package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/disco/udp"
	"github.com/sigcn/pg/vpn/nic"
)

var (
	ErrPermissionDenied error = errors.New("ipc: permission denied")
	ErrNoDaemon         error = errors.New("ipc: no daemon")
)

func getDefaultUnixSocketPath() string {
	if runtime.GOOS == "windows" {
		return "C:\\ProgramData\\pgvpn.sock"
	}
	return "/var/run/pgvpn.sock"
}

type Server struct {
	EnablePProf bool
	Vnic        *nic.VirtualNIC
	PeerStore   udp.PeerStore
	Meta        func(disco.PeerID) url.Values
}

type response[T any] struct {
	Code int    `json:"code"`
	Data T      `json:"data"`
	Msg  string `json:"msg"`
}

func (s *Server) Start(ctx context.Context, stopWG *sync.WaitGroup) error {
	unixSocketPath := getDefaultUnixSocketPath()
	_, err := os.Stat(unixSocketPath)
	if !os.IsNotExist(err) {
		r, err := net.Dial("unix", unixSocketPath)
		if err == nil {
			r.Close()
			return fmt.Errorf("%s is already in use", unixSocketPath)
		}
		os.Remove(unixSocketPath)
	}
	l, err := net.Listen("unix", unixSocketPath)
	if err != nil {
		return err
	}

	http.HandleFunc("GET /apis/p2p/v1alpha1/peers", s.handleQueryPeers)

	server := http.Server{}
	stopWG.Add(1)
	go func() {
		defer stopWG.Done()
		<-ctx.Done()
		server.Shutdown(context.Background())
		os.Remove(unixSocketPath)
	}()
	go server.Serve(l)
	return nil
}

type PeerState struct {
	ID             disco.PeerID `json:"id"`
	IPv4           string       `json:"ipv4"`
	IPv6           string       `json:"ipv6"`
	Addrs          []string     `json:"addrs"`
	LastActiveTime time.Time    `json:"last_active_time"`
	Mode           string       `json:"mode"`
}

func (s *Server) handleQueryPeers(w http.ResponseWriter, r *http.Request) {
	p2pPeers := map[disco.PeerID]*PeerState{}
	for _, p := range s.PeerStore.Peers() {
		last, ok := p2pPeers[p.PeerID]
		if !ok {
			p2pPeers[p.PeerID] = &PeerState{
				LastActiveTime: p.LastActiveTime,
				Addrs:          []string{p.Addr.String()}}
			continue
		}
		if last.LastActiveTime.Before(p.LastActiveTime) {
			last.LastActiveTime = p.LastActiveTime
		}
		last.Addrs = append(last.Addrs, p.Addr.String())
	}
	var peers []PeerState
	for _, p := range s.Vnic.Peers() {
		state := PeerState{
			ID:   disco.PeerID(p.Addr.String()),
			IPv4: p.IPv4,
			IPv6: p.IPv6,
			Mode: "RELAY",
		}
		if p2pPeer, ok := p2pPeers[disco.PeerID(p.Addr.String())]; ok {
			state.Mode = "P2P"
			state.LastActiveTime = p2pPeer.LastActiveTime
			state.Addrs = p2pPeer.Addrs
		}
		peers = append(peers, state)
	}
	json.NewEncoder(w).Encode(response[any]{Data: peers})
}

type ApiClient struct {
	httpClient *http.Client

	initClient sync.Once
}

func (c *ApiClient) init() {
	c.initClient.Do(func() {
		dialer := &net.Dialer{}
		c.httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					c, err := dialer.DialContext(ctx, "unix", getDefaultUnixSocketPath())
					if err != nil {
						if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "forbidden by its access permissions") {
							return nil, ErrPermissionDenied
						}
						if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such file or directory") {
							return nil, ErrNoDaemon
						}
					}
					return c, err
				},
			},
		}
	})
}

func (c *ApiClient) QueryPeers() ([]PeerState, error) {
	c.init()
	r, err := c.httpClient.Get("http://_/apis/p2p/v1alpha1/peers")
	if err != nil {
		return nil, errors.Unwrap(err)
	}
	var queryPeersResp response[[]PeerState]
	json.NewDecoder(r.Body).Decode(&queryPeersResp)
	if queryPeersResp.Code != 0 {
		return nil, fmt.Errorf("ENO%d: %s", queryPeersResp.Code, queryPeersResp.Msg)
	}
	return queryPeersResp.Data, nil
}
