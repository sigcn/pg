package connmux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type GenerateSeq func() uint32

var (
	seq, seqOdd, seqEven atomic.Uint32
	seqOddInit           sync.Once
	Seq                  = func() uint32 { return seq.Add(1) }
	SeqEven              = func() uint32 { return seqEven.Add(2) }
	SeqOdd               = func() uint32 {
		seqOddInit.Do(func() { seqOdd.Store(1) })
		return seqOdd.Add(2)
	}
)

type MuxConn struct {
	closeOnce sync.Once
	exit      chan struct{}
	inbound   chan []byte
	seq       uint32
	s         *MuxSession

	buf []byte
}

func (c *MuxConn) Seq() uint32 {
	return c.seq
}

func (c *MuxConn) Read(b []byte) (n int, err error) {
	select {
	case <-c.exit:
		return 0, io.EOF
	default:
	}

	if c.buf != nil {
		n = copy(b, c.buf)
		if n < len(c.buf) {
			c.buf = c.buf[n:]
		} else {
			c.buf = nil
		}
		return
	}

	wsb, ok := <-c.inbound
	if !ok {
		return 0, io.EOF
	}
	n = copy(b, wsb)
	if n < len(wsb) {
		c.buf = wsb[n:]
	}
	return
}

func (c *MuxConn) Write(p []byte) (int, error) {
	select {
	case <-c.exit:
		return 0, io.ErrClosedPipe
	default:
	}
	b := []byte{0, 0}
	b = append(b, binary.BigEndian.AppendUint16(nil, uint16(len(p)))...)
	b = append(b, binary.BigEndian.AppendUint32(nil, c.seq)...)
	b = append(b, p...)
	c.s.mut.Lock()
	defer c.s.mut.Unlock()
	n, err := c.s.c.Write(b)
	if err != nil {
		return max(0, n-8), err
	}
	return max(0, n-8), nil
}

func (c *MuxConn) Close() error {
	b := []byte{0, 1} // FIN
	b = append(b, binary.BigEndian.AppendUint16(nil, uint16(0))...)
	b = append(b, binary.BigEndian.AppendUint32(nil, c.seq)...)
	c.s.mut.Lock()
	if _, err := c.s.c.Write(b); err != nil {
		slog.Warn("MuxConnFIN", "err", err)
	}
	delete(c.s.dials, c.seq)
	delete(c.s.accepts, c.seq)
	c.s.mut.Unlock()
	c.close()
	slog.Debug("MuxConnClosed", "seq", c.seq)
	return nil
}

func (c *MuxConn) close() {
	c.closeOnce.Do(func() {
	retry:
		if len(c.inbound) != 0 {
			time.Sleep(10 * time.Microsecond) // avoid busy wait
			goto retry
		}
		close(c.exit)
		close(c.inbound)
	})
}

// LocalAddr returns the local network address, if known.
func (c *MuxConn) LocalAddr() net.Addr {
	if la, ok := c.s.c.(interface{ LocalAddr() net.Addr }); ok {
		return la.LocalAddr()
	}
	return nil
}

// RemoteAddr returns the remote network address, if known.
func (c *MuxConn) RemoteAddr() net.Addr {
	if la, ok := c.s.c.(interface{ RemoteAddr() net.Addr }); ok {
		return la.RemoteAddr()
	}
	return nil
}

func (c *MuxConn) SetDeadline(t time.Time) error {
	err1 := c.SetReadDeadline(t)
	err2 := c.SetWriteDeadline(t)
	return errors.Join(err1, err2)
}

// SetReadDeadline sets the deadline for future Read calls
// and any currently-blocked Read call.
// A zero value for t means Read will not time out.
func (c *MuxConn) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (c *MuxConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type MuxSession struct {
	mut         sync.Mutex
	closeOnce   sync.Once
	closed      atomic.Bool
	exit        chan struct{}
	accept      chan net.Conn
	generateSeq GenerateSeq
	c           io.ReadWriteCloser
	accepts     map[uint32]*MuxConn
	dials       map[uint32]*MuxConn
}

// Accept waits for and returns the next connection to the listener.
func (l *MuxSession) Accept() (net.Conn, error) {
	select {
	case <-l.exit:
		return nil, io.ErrClosedPipe
	case c, ok := <-l.accept:
		if ok {
			return c, nil
		}
		return nil, io.ErrClosedPipe
	}
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *MuxSession) Close() error {
	l.closeOnce.Do(func() {
		close(l.exit)
		close(l.accept)
		l.closed.Store(true)
	})
	return l.c.Close()
}

func (l *MuxSession) Closed() bool {
	return l.closed.Load()
}

// Addr returns the listener's network address.
func (l *MuxSession) Addr() net.Addr {
	if la, ok := l.c.(interface{ LocalAddr() net.Addr }); ok {
		return la.LocalAddr()
	}
	return nil
}

func (l *MuxSession) run() {
	defer l.Close()
	for {
		select {
		case <-l.exit:
			return
		default:
		}
		if err := l.nextFrame(); err != nil {
			slog.Error("NextFrame", "err", err)
			return
		}
	}
}

// nextFrame read a new frame
// a 8 bytes header
// 1 byte version
// 1 byte command
// 2 bytes data length
// 4 bytes seq
func (l *MuxSession) nextFrame() error {
	header := make([]byte, 8)
	_, err := io.ReadFull(l.c, header)
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	if header[0] != 0 {
		return fmt.Errorf("unsupport connmux version %d", header[0])
	}

	length := binary.BigEndian.Uint16(header[2:4])
	seq := binary.BigEndian.Uint32(header[4:8])
	cmd := header[1]
	slog.Debug("ReadHeader", "header", header)

	data := make([]byte, length)
	_, err = io.ReadFull(l.c, data)
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	if cmd == 0 {
		if c, ok := l.dials[seq]; ok {
			c.inbound <- data
			return nil
		}
		if c, ok := l.accepts[seq]; ok {
			c.inbound <- data
			return nil
		}
		l.accepts[seq] = &MuxConn{
			exit:    make(chan struct{}),
			inbound: make(chan []byte, 128),
			seq:     seq,
			s:       l,
		}
		l.accept <- l.accepts[seq]
		l.accepts[seq].inbound <- data
		return nil
	}

	if cmd == 1 {
		if c, ok := l.accepts[seq]; ok {
			c.close()
			delete(l.accepts, seq)
			slog.Debug("ServerSideMuxConnClosed", "seq", c.seq)
		}
		if c, ok := l.dials[seq]; ok {
			c.close()
			delete(l.dials, seq)
			slog.Debug("ClientSideMuxConnClosed", "seq", c.seq)
		}
		return nil
	}
	return fmt.Errorf("unsupport connmux cmd %d", cmd)
}

func (d *MuxSession) OpenStream() (net.Conn, error) {
	if d.generateSeq == nil {
		return nil, errors.New("seq generator must not nil")
	}
	c := &MuxConn{
		exit:    make(chan struct{}),
		inbound: make(chan []byte, 128),
		seq:     d.generateSeq(),
		s:       d,
	}
	d.mut.Lock()
	d.dials[c.seq] = c
	d.mut.Unlock()
	return c, nil
}

func Mux(conn io.ReadWriteCloser, generateSeq GenerateSeq) *MuxSession {
	l := &MuxSession{
		exit:        make(chan struct{}),
		c:           conn,
		generateSeq: generateSeq,
		accept:      make(chan net.Conn),
		accepts:     make(map[uint32]*MuxConn),
		dials:       make(map[uint32]*MuxConn),
	}
	go l.run()
	return l
}
