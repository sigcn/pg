package net

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// UDPListener wrap net.PacketConn as a udp listener
type UDPListener struct {
	PacketConn net.PacketConn

	buf       []byte
	initOnce  sync.Once
	closeOnce sync.Once
	udpChan   chan *udpConn

	connMap   map[string]*udpConn
	connMapMu sync.RWMutex
}

// Accept a connection-oriented udp connection
func (l *UDPListener) Accept() (net.Conn, error) {
	return l.AcceptContext(context.Background())
}

// AcceptContext accept a connection-oriented udp connection with a context
func (l *UDPListener) AcceptContext(ctx context.Context) (net.Conn, error) {
	l.init()
	select {
	case c := <-l.udpChan:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close udp listener
func (l *UDPListener) Close() error {
	if l.PacketConn == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.PacketConn.Close()
		l.connMapMu.Lock()
		defer l.connMapMu.Unlock()
		for _, c := range l.connMap {
			go c.Close()
		}
	})
	return nil
}

// Addr get listener addr
func (l *UDPListener) Addr() net.Addr {
	l.init()
	if l.PacketConn == nil {
		return nil
	}
	return l.PacketConn.LocalAddr()
}

func (l *UDPListener) init() {
	l.initOnce.Do(func() {
		l.buf = make([]byte, 65535)
		l.udpChan = make(chan *udpConn, 8)
		l.connMap = make(map[string]*udpConn)
		go l.readUDP()
	})
}

func (l *UDPListener) readUDP() {
	read := func() error {
	read:
		n, peerAddr, err := l.PacketConn.ReadFrom(l.buf)
		if err != nil {
			return err
		}
		l.connMapMu.RLock()
		conn, ok := l.connMap[peerAddr.String()]
		l.connMapMu.RUnlock()
		if ok {
			conn.inbound <- append([]byte(nil), l.buf[:n]...)
			goto read
		}
		l.connMapMu.Lock()
		conn, ok = l.connMap[peerAddr.String()]
		if ok {
			l.connMapMu.Unlock()
			conn.inbound <- append([]byte(nil), l.buf[:n]...)
			goto read
		}
		defer l.connMapMu.Unlock()
		conn = &udpConn{remoteAddr: peerAddr, c: l.PacketConn, removeConn: func() {
			l.connMapMu.Lock()
			defer l.connMapMu.Unlock()
			delete(l.connMap, peerAddr.String())
		}}
		conn.init()
		l.connMap[peerAddr.String()] = conn
		conn.inbound <- append([]byte(nil), l.buf[:n]...)
		l.udpChan <- conn
		return nil
	}
	for {
		if err := read(); err != nil {
			return
		}
	}
}

type udpConn struct {
	removeConn func()
	remoteAddr net.Addr
	c          net.PacketConn

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
		slog.Log(context.Background(), -2, "UDPConn closed", "local_addr", c.LocalAddr(), "remote_addr", c.remoteAddr)
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

var (
	_ net.Conn     = (*udpConn)(nil)
	_ net.Listener = (*UDPListener)(nil)
)
