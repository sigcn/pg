package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/p2p"
)

var (
	ErrPermissionDenied error = errors.New("ipc: permission denied")
	ErrNoDaemon         error = errors.New("ipc: no daemon")
)

type Response[T any] struct {
	Code int    `json:"code"`
	Data T      `json:"data"`
	Msg  string `json:"msg"`
}

type PeerState struct {
	ID             disco.PeerID `json:"id"`
	Hostname       string       `json:"hostname"`
	IPv4           string       `json:"ipv4"`
	IPv6           string       `json:"ipv6"`
	Addrs          []string     `json:"addrs"`
	LastActiveTime time.Time    `json:"last_active_time"`
	Mode           string       `json:"mode"`
	NAT            string       `json:"nat"`
	Version        string       `json:"version"`
	Labels         disco.Labels `json:"labels"`
}

type NodeInfo struct {
	p2p.NodeInfo
	Version string `json:"version"`
}

func GetDefaultUnixSocketPath() string {
	currentUser, err := user.Current()
	if err == nil {
		return filepath.Join(currentUser.HomeDir, ".pgvpn.sock")
	}
	if runtime.GOOS == "windows" {
		return "C:\\ProgramData\\pgvpn.sock"
	}
	return "/var/run/pgvpn.sock"
}

type ApiClient struct {
	httpClient *http.Client

	initClient sync.Once
}

func (c *ApiClient) init() {
	c.initClient.Do(func() {
		dialer := &net.Dialer{}
		unixDial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := dialer.DialContext(ctx, "unix", GetDefaultUnixSocketPath())
			if err != nil {
				if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "forbidden by its access permissions") {
					return nil, ErrPermissionDenied
				}
				if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such file or directory") {
					return nil, ErrNoDaemon
				}
			}
			return c, err
		}
		c.httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: unixDial,
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
	var queryPeersResp Response[[]PeerState]
	json.NewDecoder(r.Body).Decode(&queryPeersResp)
	if queryPeersResp.Code != 0 {
		return nil, fmt.Errorf("ENO%d: %s", queryPeersResp.Code, queryPeersResp.Msg)
	}
	return queryPeersResp.Data, nil
}

func (c *ApiClient) QueryNodeInfo() (*NodeInfo, error) {
	c.init()
	r, err := c.httpClient.Get("http://_/apis/p2p/v1alpha1/node_info")
	if err != nil {
		return nil, errors.Unwrap(err)
	}
	var resp Response[*NodeInfo]
	json.NewDecoder(r.Body).Decode(&resp)
	if resp.Code != 0 {
		return nil, fmt.Errorf("ENO%d: %s", resp.Code, resp.Msg)
	}
	return resp.Data, nil
}
