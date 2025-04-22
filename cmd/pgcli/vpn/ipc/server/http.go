package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"

	"github.com/sigcn/pg/cmd/pgcli/vpn/ipc/sdk"
	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/p2p"
	"github.com/sigcn/pg/vpn/nic"
)

type Server struct {
	Vnic       *nic.VirtualNIC
	PacketConn *p2p.PacketConn
	Version    string
}

func (s *Server) Start(ctx context.Context, stopWG *sync.WaitGroup) error {
	unixSocketPath := sdk.GetDefaultUnixSocketPath()
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
	http.HandleFunc("GET /apis/p2p/v1alpha1/node_info", s.handleQueryNodeInfo)

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

func (s *Server) usePeerRelay(peerID disco.PeerID) bool {
	for _, p := range s.PacketConn.PeerStore().Peers() {
		if p.PeerID == peerID {
			continue
		}
		meta := s.PacketConn.PeerMeta(p.PeerID)
		if meta == nil {
			continue
		}
		if _, ok := disco.Labels(meta["label"]).Get("node.nr"); ok {
			// can not as relay peer when `node.nr` label is present
			continue
		}
		peerNAT := disco.NATType(meta.Get("nat"))
		return peerNAT.Easy() || peerNAT.IP4()
	}
	return false
}

func (s *Server) handleQueryPeers(w http.ResponseWriter, r *http.Request) {
	p2pPeers := map[disco.PeerID]*sdk.PeerState{}
	for _, p := range s.PacketConn.PeerStore().Peers() {
		last, ok := p2pPeers[p.PeerID]
		if !ok {
			p2pPeers[p.PeerID] = &sdk.PeerState{
				LastActiveTime: p.LastActiveTime,
				Addrs:          []string{p.Addr.String()}}
			continue
		}
		if last.LastActiveTime.Before(p.LastActiveTime) {
			last.LastActiveTime = p.LastActiveTime
		}
		last.Addrs = append(last.Addrs, p.Addr.String())
	}
	var peers []sdk.PeerState
	for _, p := range s.Vnic.Peers() {
		state := sdk.PeerState{
			ID:       disco.PeerID(p.Addr.String()),
			Hostname: p.Meta.Get("name"),
			IPv4:     p.IPv4,
			IPv6:     p.IPv6,
			Mode:     "RELAY",
			NAT:      p.Meta.Get("nat"),
			Version:  p.Meta.Get("version"),
			Labels:   p.Meta["label"],
		}
		if p2pPeer, ok := p2pPeers[disco.PeerID(p.Addr.String())]; ok {
			state.Mode = "P2P"
			state.LastActiveTime = p2pPeer.LastActiveTime
			state.Addrs = p2pPeer.Addrs
		} else if s.usePeerRelay(disco.PeerID(p.Addr.String())) {
			state.Mode = "PEER_RELAY"
		}
		peers = append(peers, state)
	}
	json.NewEncoder(w).Encode(sdk.Response[any]{Data: peers})
}

func (s *Server) handleQueryNodeInfo(w http.ResponseWriter, r *http.Request) {
	ni := sdk.NodeInfo{
		NodeInfo: s.PacketConn.NodeInfo(),
		Version:  s.Version,
	}
	json.NewEncoder(w).Encode(sdk.Response[any]{Data: ni})
}
