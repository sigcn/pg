package gvisor

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

var _ net.Listener = (*udpListener)(nil)
var _ net.Conn = (*udpConn)(nil)

type udpConn struct {
	removeConn func()
	remoteAddr net.Addr
	c          *gonet.UDPConn

	closeOnce      sync.Once
	inbound        chan []byte
	closeChan      chan struct{}
	lastActiveTime atomic.Value
}

func (c *udpConn) init() {
	c.inbound = make(chan []byte, 512)
	c.closeChan = make(chan struct{})
	c.lastActiveTime.Store(time.Now())
	ticker := time.NewTicker(6 * time.Second)
	go func() { // create a timer to trace timeout udp conn, and close it
		defer ticker.Stop()
		for range ticker.C {
			if time.Since(c.lastActiveTime.Load().(time.Time)) > 10*time.Second {
				c.Close()
				break
			}
		}
	}()
}

func (c *udpConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.inbound:
		c.lastActiveTime.Store(time.Now())
		return copy(p, b), nil
	case <-c.closeChan:
		return 0, net.ErrClosed
	}
}

func (c *udpConn) Write(p []byte) (int, error) {
	c.lastActiveTime.Store(time.Now())
	return c.c.WriteTo(p, c.remoteAddr)
}

func (c *udpConn) LocalAddr() net.Addr {
	return c.c.LocalAddr()
}

func (c *udpConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *udpConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		close(c.inbound)
		c.removeConn()
		slog.Log(context.Background(), -2, "[gVisor] UDPConn closed", "local_addr", c.LocalAddr(), "remote_addr", c.remoteAddr)
	})
	return nil
}

func (c *udpConn) SetDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

func (c *udpConn) SetReadDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

func (c *udpConn) SetWriteDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

type udpListener struct {
	addr tcpip.FullAddress
	s    *stack.Stack

	buf       []byte
	c         *gonet.UDPConn
	initErr   error
	initOnce  sync.Once
	closeOnce sync.Once

	connMap   map[net.Addr]*udpConn
	connMapMu sync.RWMutex
}

func (l *udpListener) init() {
	l.initOnce.Do(func() {
		var wq waiter.Queue
		var ep tcpip.Endpoint
		var err tcpip.Error
		if net.IP(l.addr.Addr.AsSlice()).To4() != nil {
			ep, err = l.s.NewEndpoint(udp.ProtocolNumber, ipv4.ProtocolNumber, &wq)
		} else {
			ep, err = l.s.NewEndpoint(udp.ProtocolNumber, ipv6.ProtocolNumber, &wq)
		}
		if err != nil {
			l.initErr = errors.New(err.String())
			return
		}
		err = ep.Bind(l.addr)
		if err != nil {
			l.initErr = errors.New(err.String())
			return
		}
		l.buf = make([]byte, 65535)
		l.connMap = make(map[net.Addr]*udpConn)
		l.c = gonet.NewUDPConn(&wq, ep)
	})
}

func (l *udpListener) Accept() (net.Conn, error) {
	l.init()
	if l.initErr != nil {
		return nil, l.initErr
	}
read:
	n, peerAddr, err := l.c.ReadFrom(l.buf)
	if err != nil {
		return nil, err
	}
	l.connMapMu.RLock()
	conn, ok := l.connMap[peerAddr]
	l.connMapMu.RUnlock()
	if ok {
		conn.inbound <- append([]byte(nil), l.buf[:n]...)
		goto read
	}
	l.connMapMu.Lock()
	conn, ok = l.connMap[peerAddr]
	if ok {
		l.connMapMu.Unlock()
		conn.inbound <- append([]byte(nil), l.buf[:n]...)
		goto read
	}
	defer l.connMapMu.Unlock()
	conn = &udpConn{remoteAddr: peerAddr, c: l.c, removeConn: func() {
		l.connMapMu.Lock()
		defer l.connMapMu.Unlock()
		delete(l.connMap, peerAddr)
	}}
	conn.init()
	l.connMap[peerAddr] = conn
	conn.inbound <- append([]byte(nil), l.buf[:n]...)
	return conn, nil
}

func (l *udpListener) Close() error {
	if l.c == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.c.Close()
		l.connMapMu.Lock()
		defer l.connMapMu.Unlock()
		for _, c := range l.connMap {
			go c.Close()
		}
	})
	return nil
}

func (l *udpListener) Addr() net.Addr {
	l.init()
	if l.c == nil {
		return nil
	}
	return l.c.LocalAddr()
}
