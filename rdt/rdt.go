package rdt

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type nck struct {
	no      uint32
	missing []uint32
}

var _ net.Conn = (*rdtConn)(nil)

type rdtConn struct {
	cfg        Config
	window     uint32
	frameSize  int
	c          net.PacketConn
	remoteAddr net.Addr

	exit       chan struct{}
	inbound    chan []byte
	nck        chan nck
	fin        chan uint32
	sendEvent  chan struct{}
	inboundBuf []byte

	convID uint32
	recvNO uint32
	sentNO uint32
	ackNO  uint32
	rs     atomic.Uint32

	recvPool  map[uint32][]byte
	recvMutex sync.RWMutex
	sendPool  map[uint32][]byte
	sendMutex sync.RWMutex

	state atomic.Int32 // 0 Established 1 CLOSED 2 FIN_WAIT

	closeOnce sync.Once
}

// Read reads data from the connection.
// Read can be made to time out and return an error after a fixed
// time limit; see SetDeadline and SetReadDeadline.
func (c *rdtConn) Read(b []byte) (n int, err error) {
	if c.inboundBuf == nil {
		select {
		case <-c.exit:
			err = &net.OpError{
				Op:     "read",
				Net:    c.c.LocalAddr().Network(),
				Source: c.c.LocalAddr(),
				Addr:   c.remoteAddr,
				Err:    errors.New("closed"),
			}
			return
		case pkt, ok := <-c.inbound:
			if !ok {
				return 0, &net.OpError{
					Op:     "read",
					Net:    c.c.LocalAddr().Network(),
					Source: c.c.LocalAddr(),
					Addr:   c.remoteAddr,
					Err:    errors.New("closed"),
				}
			}
			c.inboundBuf = pkt
		}
	}
	n = copy(b, c.inboundBuf)
	if n == len(c.inboundBuf) {
		c.inboundBuf = nil
		return
	}
	c.inboundBuf = c.inboundBuf[n:]
	return
}

// Write writes data to the connection.
// Write can be made to time out and return an error after a fixed
// time limit; see SetDeadline and SetWriteDeadline.
func (c *rdtConn) Write(b []byte) (n int, err error) {
	if c.state.Load() > 0 {
		err = &net.OpError{
			Op:     "write",
			Net:    c.c.LocalAddr().Network(),
			Source: c.c.LocalAddr(),
			Addr:   c.remoteAddr,
			Err:    errors.New("closed"),
		}
		return
	}
	for i := range int(math.Ceil(float64(len(b)) / float64(c.frameSize))) {
		start := i * c.frameSize
		length := c.frameSize
		if len(b)-start < c.frameSize {
			length = len(b) - start
		}
		c.sendMutex.Lock()
		if len(c.sendPool) >= int(c.window) {
			c.sendMutex.Unlock()
			for {
				if _, ok := <-c.sendEvent; !ok {
					err = &net.OpError{
						Op:     "write",
						Net:    c.c.LocalAddr().Network(),
						Source: c.c.LocalAddr(),
						Addr:   c.remoteAddr,
						Err:    errors.New("closed"),
					}
					return
				}
				c.sendMutex.Lock()
				if len(c.sendPool) >= int(c.window) {
					c.sendMutex.Unlock()
					continue
				}
				break
			}
		}
		no := c.sentNO + 1
		pkt := c.buildFrame(0, no, uint16(length), b[start:start+length])
		c.sendPool[no] = pkt
		c.sentNO++
		c.sendMutex.Unlock()
		c.send(pkt)
	}
	n = len(b)
	return
}

// Close closes the connection.
// Any blocked Read or Write operations will be unblocked and return errors.
func (c *rdtConn) Close() error {
	c.closeOnce.Do(func() {
		c.state.Store(2)
		c.sendFIN()
		c.state.Store(1)
		close(c.exit)
		close(c.inbound)
		close(c.nck)
		close(c.fin)
		close(c.sendEvent)
		c.inboundBuf = nil
		c.recvNO = 0
		c.sentNO = 0
		c.sendMutex.Lock()
		clear(c.sendPool)
		c.sendMutex.Unlock()
		c.recvMutex.Lock()
		clear(c.recvPool)
		c.recvMutex.Unlock()
	})
	return nil
}

func (c *rdtConn) Flush() error {
	sentNO := c.sentNO
	for i := 0; i < 100; i++ {
		c.askNCK(sentNO)
		time.Sleep(50 * time.Millisecond)
		if c.ackNO >= sentNO {
			return nil
		}
	}
	return errors.New("timeout")
}

// LocalAddr returns the local network address, if known.
func (c *rdtConn) LocalAddr() net.Addr {
	return c.c.LocalAddr()
}

// RemoteAddr returns the remote network address, if known.
func (c *rdtConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// SetDeadline sets the read and write deadlines associated
// with the connection. It is equivalent to calling both
// SetReadDeadline and SetWriteDeadline.
//
// A deadline is an absolute time after which I/O operations
// fail instead of blocking. The deadline applies to all future
// and pending I/O, not just the immediately following call to
// Read or Write. After a deadline has been exceeded, the
// connection can be refreshed by setting a deadline in the future.
//
// If the deadline is exceeded a call to Read or Write or to other
// I/O methods will return an error that wraps os.ErrDeadlineExceeded.
// This can be tested using errors.Is(err, os.ErrDeadlineExceeded).
// The error's Timeout method will return true, but note that there
// are other possible errors for which the Timeout method will
// return true even if the deadline has not been exceeded.
//
// An idle timeout can be implemented by repeatedly extending
// the deadline after successful Read or Write calls.
//
// A zero value for t means I/O operations will not time out.
func (c *rdtConn) SetDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (c *rdtConn) SetReadDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (c *rdtConn) SetWriteDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

func (c *rdtConn) resend(nck nck) {
	missing := map[uint32]struct{}{}
	for _, n := range nck.missing {
		c.sendMutex.RLock()
		pkt, ok := c.sendPool[n]
		c.sendMutex.RUnlock()
		if ok {
			c.send(pkt)
			c.rs.Add(1)
		}
		missing[n] = struct{}{}
	}
	c.sendMutex.Lock()
	for k := range c.sendPool {
		if _, ok := missing[k]; !ok && k <= nck.no {
			delete(c.sendPool, k)
		}
	}
	c.sendMutex.Unlock()
	go func() {
		defer func() { recover() }()
		c.sendEvent <- struct{}{}
	}()
}

func (c *rdtConn) send(pkt []byte) {
	no := binary.BigEndian.Uint32(pkt[1:5])
	slog.Debug("RDTSend", "cmd", pkt[0], "no", no, "peer", c.remoteAddr, "len", len(pkt))
	c.c.WriteTo(pkt, c.RemoteAddr())
}

func (c *rdtConn) buildFrame(cmd byte, no uint32, length uint16, data []byte) []byte {
	pkt := []byte{cmd}
	pkt = append(pkt, binary.BigEndian.AppendUint32(nil, no)...)
	pkt = append(pkt, binary.BigEndian.AppendUint32(nil, c.convID)...)
	pkt = append(pkt, binary.BigEndian.AppendUint16(nil, length)...)
	pkt = append(pkt, data...)
	return pkt
}

func (c *rdtConn) recv(pkt []byte) {
	no := binary.BigEndian.Uint32(pkt[1:5])
	l := binary.BigEndian.Uint16(pkt[9:11])
	slog.Debug("RDTRecv", "cmd", pkt[0], "no", no, "peer", c.remoteAddr, "len", len(pkt))
	switch pkt[0] {
	case 0: // DATA
		c.recvData(no, pkt[11:l+11])
	case 1: // QueryNCK
		c.sendNCK(no)
	case 2: // NCK
		nck := nck{no: no}
		for i := range l / 4 {
			s := 11 + i*4
			nck.missing = append(nck.missing, binary.BigEndian.Uint32(pkt[s:s+4]))
		}
		if len(nck.missing) == 0 && nck.no > c.ackNO {
			c.ackNO = nck.no
		}
		c.resend(nck)
	case 21: // FIN
		if c.recvNO < no {
			c.sendNCK(no)
			return
		}
		c.send(c.buildFrame(22, no, 0, nil))
	case 22: // FINACK
		c.fin <- no
	}
}

func (c *rdtConn) recvData(no uint32, data []byte) {
	if c.state.Load() == 1 {
		slog.Warn("drop packet for closed conn")
		return
	}
	if no%c.window == 0 {
		c.sendNCK(min(c.recvNO+c.window, no))
	}
	if no <= c.recvNO {
		return
	}
	if no == c.recvNO+1 {
		c.inbound <- data
		c.recvNO++
		for {
			if k, ok := c.recvPool[c.recvNO+1]; ok {
				c.inbound <- k
				c.recvNO++
				delete(c.recvPool, c.recvNO)
				continue
			}
			break
		}
		return
	}
	if no-c.recvNO > c.window*2 {
		c.sendNCK(c.recvNO + c.window)
		return
	}
	c.recvMutex.Lock()
	c.recvPool[no] = data
	c.recvMutex.Unlock()
}

func (c *rdtConn) askNCK(no uint32) {
	c.send(c.buildFrame(1, no, 0, nil))
}

func (c *rdtConn) sendNCK(no uint32) {
	var missing uint16
	var noData []byte
	for i := c.recvNO + 1; i <= no; i++ {
		c.recvMutex.RLock()
		_, ok := c.recvPool[i]
		c.recvMutex.RUnlock()
		if !ok {
			missing++
			noData = append(noData, binary.BigEndian.AppendUint32(nil, uint32(i))...)
		}
	}
	if missing > uint16(c.window) {
		slog.Debug("NCKOverflow", "missing", missing, "ackcount", c.window, "no", no, "recvno", c.recvNO)
		return
	}
	c.send(c.buildFrame(2, no, uint16(missing*4), noData))
}

func (c *rdtConn) sendFIN() error {
	exit := make(chan struct{})
	go func() {
		for range 5 {
			select {
			case <-exit:
				return
			default:
			}
			c.send(c.buildFrame(21, c.sentNO, 0, nil))
			time.Sleep(100 * time.Millisecond)
		}
		c.fin <- 0
	}()
	for {
		fin, ok := <-c.fin
		if !ok {
			return io.ErrClosedPipe
		}
		if fin == 0 {
			return errors.New("timeout")
		}
		if fin != c.sentNO {
			continue
		}
		close(exit)
		return nil
	}
}

func (c *rdtConn) run() {
	for {
		select {
		case <-c.exit:
			return
		default:
		}
		time.Sleep(c.cfg.Interval)
		c.sendMutex.RLock()
		count := len(c.sendPool)
		c.sendMutex.RUnlock()
		if count > 0 {
			c.askNCK(c.sentNO)
		}
	}
}

// RDTListener reliable data transmission listener
type RDTListener struct {
	convNO       uint32
	cfg          Config
	c            net.PacketConn
	accept       chan *rdtConn
	connMap      map[string]*rdtConn
	connMapMutex sync.RWMutex
	exitSig      chan struct{}
}

// Accept accept a connection from addr (0RTT)
func (l *RDTListener) Accept() (net.Conn, error) {
	select {
	case c, ok := <-l.accept:
		if !ok {
			return nil, io.ErrClosedPipe
		}
		return c, nil
	case <-l.exitSig:
		return nil, io.ErrClosedPipe
	}
}

func (l *RDTListener) Addr() net.Addr {
	return l.c.LocalAddr()
}

func (l *RDTListener) Close() error {
	close(l.exitSig)
	l.connMapMutex.RLock()
	for _, v := range l.connMap {
		v.Close()
	}
	l.connMapMutex.RUnlock()
	return l.c.Close()
}

// OpenStream open a connection to addr (0RTT)
func (l *RDTListener) OpenStream(addr net.Addr) (net.Conn, error) {
	l.connMapMutex.Lock()
	defer l.connMapMutex.Unlock()
	l.convNO++
	c := rdtConn{
		cfg:        l.cfg,
		window:     uint32((l.cfg.MTU - 11) / 4),
		frameSize:  l.cfg.MTU - 11,
		c:          l.c,
		remoteAddr: addr,
		exit:       make(chan struct{}),
		inbound:    make(chan []byte, 1024),
		nck:        make(chan nck),
		fin:        make(chan uint32),
		convID:     l.convNO,
		sendEvent:  make(chan struct{}),
		recvPool:   map[uint32][]byte{},
		sendPool:   map[uint32][]byte{},
	}
	l.connMap[fmt.Sprintf("%s:00000000", addr.String())] = &c
	go c.run()
	return &c, nil
}

func (l *RDTListener) runPacketReadLoop() {
	buf := make([]byte, 1500)
	for {
		select {
		case <-l.exitSig:
			return
		default:
		}
		n, addr, err := l.c.ReadFrom(buf)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				return
			}
			panic(err)
		}
		if n < 11 {
			slog.Error("RDT received invalid packet")
			continue
		}
		connKey := fmt.Sprintf("%s:%x", addr.String(), buf[5:9])
		l.connMapMutex.RLock()
		c, ok := l.connMap[connKey]
		l.connMapMutex.RUnlock()
		if !ok {
			l.connMapMutex.Lock()
			if conn, ok := l.connMap[connKey]; !ok || conn.state.Load() > 0 {
				c := rdtConn{
					cfg:        l.cfg,
					window:     uint32((l.cfg.MTU - 11) / 4),
					frameSize:  l.cfg.MTU - 11,
					c:          l.c,
					remoteAddr: addr,
					exit:       make(chan struct{}),
					inbound:    make(chan []byte, 1024),
					nck:        make(chan nck),
					fin:        make(chan uint32),
					sendEvent:  make(chan struct{}),
					recvPool:   map[uint32][]byte{},
					sendPool:   map[uint32][]byte{},
				}
				l.connMap[connKey] = &c
				l.accept <- &c
				go c.run()
			}
			c = l.connMap[connKey]
			l.connMapMutex.Unlock()
		}
		c.recv(append([]byte(nil), buf[:n]...))
	}
}

func Listen(conn net.PacketConn, opts ...Option) (*RDTListener, error) {
	cfg := Config{
		MTU:      1428,
		Interval: 50 * time.Millisecond,
	}

	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	l := RDTListener{
		cfg:     cfg,
		c:       conn,
		accept:  make(chan *rdtConn, 512),
		connMap: map[string]*rdtConn{},
		exitSig: make(chan struct{}),
	}

	if len(cfg.StatsServerListen) > 0 {
		httpListener, err := net.Listen("tcp", cfg.StatsServerListen)
		if err != nil {
			return nil, err
		}
		go runStatsHTTPServer(httpListener, &l)
	}

	go l.runPacketReadLoop()
	return &l, nil
}
