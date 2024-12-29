package udp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"tailscale.com/net/stun"
)

type stunRoundTripper struct {
	stunResponseMapMutex sync.RWMutex
	doInit               sync.Once

	stunResponseMap map[string]chan stunResponse // key is stun txid
}

func (rt *stunRoundTripper) init() {
	rt.doInit.Do(func() {
		rt.stunResponseMap = make(map[string]chan stunResponse)
	})
}

func (rt *stunRoundTripper) roundTrip(ctx context.Context, udpConn *net.UDPConn, stunServer string) (*net.UDPAddr, error) {
	rt.init()
	txID := stun.NewTxID()
	ch := make(chan stunResponse)
	defer close(ch)
	rt.stunResponseMapMutex.Lock()
	rt.stunResponseMap[string(txID[:])] = ch
	rt.stunResponseMapMutex.Unlock()

	uaddr, err := rt.resolveUDPAddr(ctx, stunServer)
	if err != nil {
		return nil, fmt.Errorf("resolve stun addr: %w", err)
	}
	_, err = udpConn.WriteToUDP(stun.Request(txID), uaddr)
	if err != nil {
		return nil, fmt.Errorf("write udp: %w", err)
	}

	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	select {
	case r := <-ch:
		return r.addr, nil
	case <-timeout.C:
		return nil, os.ErrDeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stunRoundTripper) resolveUDPAddr(ctx context.Context, addr string) (udpAddr *net.UDPAddr, err error) {
	var closeOnce sync.Once
	ch := make(chan struct{})
	defer closeOnce.Do(func() { close(ch) })
	go func() {
		udpAddr, err = net.ResolveUDPAddr("udp", addr)
		closeOnce.Do(func() { close(ch) })
	}()
	select {
	case <-ch:
		return
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stunRoundTripper) recvResponse(b []byte, peerAddr net.Addr) {
	c.init()
	txid, saddr, err := stun.ParseResponse(b)
	if err != nil {
		slog.Error("[STUN] ParseResponse", "stun", peerAddr, "err", fmt.Errorf("parse: %w", err))
		return
	}
	c.stunResponseMapMutex.RLock()
	if r, ok := c.stunResponseMap[string(txid[:])]; ok {
		c.stunResponseMapMutex.RUnlock()
		addr, err := net.ResolveUDPAddr("udp", saddr.String())
		if err != nil {
			slog.Error("[STUN] ParseResponse", "stun", peerAddr, "err", fmt.Errorf("resolve udp addr: %w", err))
			return
		}
		resp := stunResponse{txid: string(txid[:]), addr: addr}
		defer func() { recover() }()
		select {
		case r <- resp:
		default:
		}
		slog.Log(context.Background(), -2, "[STUN] RecvResponse", "from", peerAddr, "pub_addr", addr)
		return
	}
	c.stunResponseMapMutex.RUnlock()
	slog.Log(context.Background(), -2, "[STUN] RecvResponse", "from", peerAddr)
}
