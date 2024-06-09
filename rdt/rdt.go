package rdt

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Established = 0
	FIN_WAIT1   = 2
	FIN_WAIT2   = 3
	CLOSED      = 5
)

type nck struct {
	no      uint32
	missing []uint32
}

var _ net.Conn = (*rdtConn)(nil)

type rdtConn struct {
	server     bool
	cfg        Config
	window     uint32
	frameSize  int
	c          net.PacketConn
	remoteAddr net.Addr

	exit       chan struct{}
	inbound    chan []byte
	nck        chan nck
	nckQuery   chan uint32
	fin        chan uint32
	finack     chan uint32
	sendEvent  chan struct{}
	inboundBuf []byte

	recvNO, sentNO, ackNO uint32

	recvPool  map[uint32][]byte
	recvMutex sync.RWMutex
	sendPool  map[uint32][]byte
	sendMutex sync.RWMutex

	rs    atomic.Uint32
	state atomic.Int32 // 0 Established 2 FIN_WAIT 5 CLOSED

	closeOnce sync.Once

	rClosed, wClosed *net.OpError
}

// Read reads data from the connection.
// Read can be made to time out and return an error after a fixed
// time limit; see SetDeadline and SetReadDeadline.
func (c *rdtConn) Read(b []byte) (n int, err error) {
	if c.inboundBuf == nil {
		select {
		case <-c.exit:
			err = c.rClosed
			return
		case pkt, ok := <-c.inbound:
			if !ok {
				return 0, c.rClosed
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
		err = c.wClosed
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
					err = c.wClosed
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
		c.state.Store(FIN_WAIT1)
		c.sendFIN()
		c.state.Store(FIN_WAIT2)
		go func() {
			wait := time.NewTimer(15 * time.Second)
			select {
			case <-wait.C:
				slog.Warn("FIN_WAIT2 timeout")
			case <-c.fin:
			}
			c.state.Store(CLOSED)
			close(c.fin)
			close(c.nckQuery)
			c.recvNO = 0
			c.recvMutex.Lock()
			clear(c.recvPool)
			c.recvMutex.Unlock()
		}()
		close(c.exit)
		close(c.inbound)
		close(c.nck)
		close(c.finack)
		close(c.sendEvent)
		c.inboundBuf = nil
		c.sentNO = 0
		c.sendMutex.Lock()
		clear(c.sendPool)
		c.sendMutex.Unlock()
	})
	return nil
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
	defer func() { recover() }()
	c.sendEvent <- struct{}{}
}

func (c *rdtConn) send(pkt []byte) {
	if c.server {
		pkt[0] |= 0x80
	}
	no := binary.BigEndian.Uint32(pkt[1:5])
	slog.Debug("RDTSend", "cmd", pkt[0], "no", no, "peer", c.remoteAddr, "len", len(pkt))
	c.c.WriteTo(pkt, c.RemoteAddr())
}

func (c *rdtConn) buildFrame(cmd byte, no uint32, length uint16, data []byte) []byte {
	pkt := []byte{cmd}
	pkt = append(pkt, binary.BigEndian.AppendUint32(nil, no)...)
	pkt = append(pkt, binary.BigEndian.AppendUint16(nil, length)...)
	pkt = append(pkt, data...)
	return pkt
}

func (c *rdtConn) recv(pkt []byte) {
	defer func() {
		if err := recover(); err != nil {
			slog.Debug("Recv", "recover", err)
		}
	}()
	no := binary.BigEndian.Uint32(pkt[1:5])
	l := binary.BigEndian.Uint16(pkt[5:7])
	slog.Debug("RDTRecv", "cmd", pkt[0], "no", no, "peer", c.remoteAddr, "len", len(pkt))
	switch pkt[0] {
	case 0: // DATA
		c.recvData(no, pkt[7:l+7])
	case 1: // QueryNCK
		c.nckQuery <- no
	case 2: // NCK
		nck := nck{no: no}
		for i := range l / 4 {
			s := 7 + i*4
			nck.missing = append(nck.missing, binary.BigEndian.Uint32(pkt[s:s+4]))
		}
		if len(nck.missing) == 0 && nck.no > c.ackNO {
			c.ackNO = nck.no
		}
		c.nck <- nck
	case 21: // FIN
		if c.recvNO < no {
			c.sendNCK(no)
			return
		}
		c.send(c.buildFrame(22, no, 0, nil))
		c.fin <- no
	case 22: // FINACK
		c.finack <- no
	}
}

func (c *rdtConn) recvData(no uint32, data []byte) {
	if c.state.Load() == CLOSED {
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
			c.recvMutex.RLock()
			k, ok := c.recvPool[c.recvNO+1]
			c.recvMutex.RUnlock()
			if ok {
				c.recvMutex.Lock()
				delete(c.recvPool, c.recvNO)
				c.recvMutex.Unlock()
				c.inbound <- k
				c.recvNO++
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
		defer func() {
			if err := recover(); err != nil {
				slog.Debug("SendFIN", "recover", err)
			}
		}()
		for range 5 {
			select {
			case <-exit:
				return
			default:
			}
			c.send(c.buildFrame(21, c.sentNO, 0, nil))
			time.Sleep(50 * time.Millisecond)
		}
		c.finack <- 0
	}()
	for {
		finack, ok := <-c.finack
		if !ok {
			return io.ErrClosedPipe
		}
		if finack == 0 {
			return errors.New("timeout")
		}
		if finack != c.sentNO {
			continue
		}
		close(exit)
		return nil
	}
}

func (c *rdtConn) runCheckLoop() {
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
			c.askNCK(min(c.sentNO, c.ackNO+c.window))
		}
	}
}

func (c *rdtConn) runNCKLoop() {
	for {
		select {
		case <-c.exit:
			return
		case nck, ok := <-c.nck:
			if !ok {
				return
			}
			c.resend(nck)
		}
	}
}

func (c *rdtConn) runNCKQueryLoop() {
	for {
		select {
		case <-c.exit:
			return
		case q, ok := <-c.nckQuery:
			if !ok {
				return
			}
			c.sendNCK(q)
		}
	}
}

func (c *rdtConn) startEventLoopGroup() {
	go c.runNCKLoop()
	go c.runNCKQueryLoop()
	go c.runCheckLoop()
}

// RDTListener reliable data transmission listener
type RDTListener struct {
	cfg                Config
	c                  net.PacketConn
	accept             chan *rdtConn
	acceptConnMap      map[string]*rdtConn
	acceptConnMapMutex sync.RWMutex
	openConnMap        map[string]*rdtConn
	openConnMapMutex   sync.RWMutex
	exitSig            chan struct{}
}

// Accept accept a connection from addr (0RTT)
func (l *RDTListener) Accept() (net.Conn, error) {
	err := &net.OpError{
		Op:  "accept",
		Net: l.c.LocalAddr().Network(),
		Err: errors.New("closed"),
	}
	select {
	case c, ok := <-l.accept:
		if !ok {
			return nil, err
		}
		return c, nil
	case <-l.exitSig:
		return nil, err
	}
}

func (l *RDTListener) Addr() net.Addr {
	return l.c.LocalAddr()
}

func (l *RDTListener) Close() error {
	close(l.exitSig)
	l.acceptConnMapMutex.RLock()
	for _, v := range l.acceptConnMap {
		v.Close()
	}
	l.acceptConnMapMutex.RUnlock()
	l.openConnMapMutex.RLock()
	for _, v := range l.openConnMap {
		v.Close()
	}
	l.openConnMapMutex.RUnlock()
	return l.c.Close()
}

// OpenStream open a connection to addr (0RTT)
func (l *RDTListener) OpenStream(addr net.Addr) (net.Conn, error) {
	c := l.newConn(addr)
	l.openConnMapMutex.Lock()
	defer l.openConnMapMutex.Unlock()
	l.openConnMap[addr.String()] = c
	c.startEventLoopGroup()
	return c, nil
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
		if n < 7 {
			slog.Error("RDT received invalid packet")
			continue
		}
		l.recvPacket(append([]byte(nil), buf[:n]...), addr)
	}
}

func (l *RDTListener) recvPacket(pkt []byte, addr net.Addr) {
	if 0x80&pkt[0] == 0x80 {
		pkt[0] = pkt[0] << 1 >> 1
		l.openConnMapMutex.RLock()
		conn, ok := l.openConnMap[addr.String()]
		l.openConnMapMutex.RUnlock()
		if ok {
			conn.recv(pkt)
			return
		}
	}
	l.acceptConnMapMutex.RLock()
	conn, ok := l.acceptConnMap[addr.String()]
	l.acceptConnMapMutex.RUnlock()
	if ok && conn.state.Load() < CLOSED {
		conn.recv(pkt)
		return
	}
	l.acceptConnMapMutex.Lock()
	defer l.acceptConnMapMutex.Unlock()
	conn, ok = l.acceptConnMap[addr.String()]
	if ok && conn.state.Load() < CLOSED {
		conn.recv(pkt)
		return
	}
	if pkt[0] != 0 {
		return
	}
	conn = l.newConn(addr)
	conn.server = true
	l.acceptConnMap[addr.String()] = conn
	l.accept <- conn
	conn.startEventLoopGroup()
	conn.recv(pkt)
}

func (l *RDTListener) newConn(remoteAddr net.Addr) *rdtConn {
	return &rdtConn{
		cfg:        l.cfg,
		window:     uint32((l.cfg.MTU - 7) / 4),
		frameSize:  l.cfg.MTU - 7,
		c:          l.c,
		remoteAddr: remoteAddr,
		exit:       make(chan struct{}),
		inbound:    make(chan []byte, 1024),
		nck:        make(chan nck, 256),
		nckQuery:   make(chan uint32, 256),
		fin:        make(chan uint32, 5),
		finack:     make(chan uint32, 5),
		sendEvent:  make(chan struct{}),
		recvPool:   map[uint32][]byte{},
		sendPool:   map[uint32][]byte{},
		rClosed: &net.OpError{
			Op:     "read",
			Net:    l.c.LocalAddr().Network(),
			Source: l.c.LocalAddr(),
			Addr:   remoteAddr,
			Err:    errors.New("closed"),
		},
		wClosed: &net.OpError{
			Op:     "write",
			Net:    l.c.LocalAddr().Network(),
			Source: l.c.LocalAddr(),
			Addr:   remoteAddr,
			Err:    errors.New("closed"),
		},
	}
}

func Listen(conn net.PacketConn, opts ...Option) (*RDTListener, error) {
	cfg := Config{
		MTU:      1428,
		Interval: 100 * time.Millisecond,
	}

	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}

	l := RDTListener{
		cfg:           cfg,
		c:             conn,
		accept:        make(chan *rdtConn, 512),
		openConnMap:   map[string]*rdtConn{},
		acceptConnMap: map[string]*rdtConn{},
		exitSig:       make(chan struct{}),
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
