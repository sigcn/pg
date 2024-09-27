package connmux

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	N "github.com/sigcn/pg/net"
)

const (
	CMD_DATA = 0
	CMD_FIN  = 1

	HEADER_LEN = 8
)

type SeqGen interface {
	GenSeq() uint32
}

type stdSeqGen struct {
	seq     atomic.Uint32
	delta   uint32
	initSeq uint32
	init    sync.Once
}

func (gen *stdSeqGen) GenSeq() uint32 {
	gen.init.Do(func() {
		gen.seq.Store(gen.initSeq)
	})
	return gen.seq.Add(gen.delta)
}

var (
	Seq     = NewSeq()
	SeqEven = NewSeqEven()
	SeqOdd  = NewSeqOdd()
)

func NewSeqEven() SeqGen {
	return &stdSeqGen{delta: 2}
}

func NewSeqOdd() SeqGen {
	return &stdSeqGen{initSeq: 1, delta: 2}
}

func NewSeq() SeqGen {
	return &stdSeqGen{delta: 1}
}

type MuxConn struct {
	closeOnce sync.Once
	fin       chan struct{}
	finWait   chan struct{}
	inbound   chan []byte
	seq       uint32
	s         *MuxSession

	buf []byte

	deadlineRead N.Deadline
}

func (c *MuxConn) Seq() uint32 {
	return c.seq
}

func (c *MuxConn) Read(b []byte) (n int, err error) {
	if c.buf != nil {
		n = copy(b, c.buf)
		if n < len(c.buf) {
			c.buf = c.buf[n:]
		} else {
			c.buf = nil
		}
		return
	}

	select {
	case _, ok := <-c.deadlineRead.Deadline():
		if !ok {
			return 0, io.EOF
		}
		return 0, N.ErrDeadline
	case wsb, ok := <-c.inbound:
		if !ok {
			return 0, io.EOF
		}
		n = copy(b, wsb)
		if n < len(wsb) {
			c.buf = wsb[n:]
		}
		return
	}
}

func (c *MuxConn) Write(p []byte) (int, error) {
	b := []byte{0, 0}
	b = append(b, binary.BigEndian.AppendUint16(nil, uint16(len(p)))...)
	b = append(b, binary.BigEndian.AppendUint32(nil, c.seq)...)
	b = append(b, p...)
	c.s.w.Lock()
	defer c.s.w.Unlock()
	select {
	case <-c.fin:
		return 0, io.ErrClosedPipe
	default:
	}
	n, err := c.s.c.Write(b)
	if err != nil {
		return max(0, n-HEADER_LEN), err
	}
	return max(0, n-HEADER_LEN), nil
}

func (c *MuxConn) Close() error {
	closeConn := func() {
		close(c.fin) // disable write

		b := []byte{0, 1}
		b = append(b, binary.BigEndian.AppendUint16(nil, uint16(0))...)
		b = append(b, binary.BigEndian.AppendUint32(nil, c.seq)...)

		c.s.w.Lock()
		if _, err := c.s.c.Write(b); err != nil { // send FIN
			slog.Warn("MuxConnFIN", "err", err)
		}
		c.s.w.Unlock()
		slog.Debug("MuxConnClosed", "seq", c.seq, "state", "CLOSE_WAIT")

		go func() { // FIN WAIT
			timeout := time.NewTimer(30 * time.Second)
			defer timeout.Stop()
			select {
			case <-c.finWait:
			case <-timeout.C:
			}
			c.s.r.Lock()
			delete(c.s.dials, c.seq)
			delete(c.s.accepts, c.seq)
			c.s.r.Unlock()

			for range 20 { // wait read done
				if len(c.inbound) == 0 {
					break
				}
				time.Sleep(10 * time.Millisecond) // avoid busy wait
			}

			close(c.inbound) // disable read
			c.deadlineRead.Close()
			slog.Debug("MuxConnClosed", "seq", c.seq, "state", "CLOSED")
		}()
	}
	c.closeOnce.Do(closeConn)
	return nil
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
	c.deadlineRead.SetDeadline(t)
	return nil
}

// SetWriteDeadline sets the deadline for future Write calls
// and any currently-blocked Write call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means Write will not time out.
func (c *MuxConn) SetWriteDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

type MuxSession struct {
	r, w      sync.RWMutex
	closeOnce sync.Once
	closed    atomic.Bool
	exit      chan struct{}
	accept    chan net.Conn
	seqGen    SeqGen
	c         io.ReadWriteCloser
	accepts   map[uint32]*MuxConn
	dials     map[uint32]*MuxConn
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
// a frame consists of an 8-byte header and data
// | VER | CMD | LEN | SEQ | DATA |
//
// 1 byte version
// 1 byte command
// 2 bytes data length
// 4 bytes seq
func (l *MuxSession) nextFrame() error {
	header := make([]byte, HEADER_LEN)
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
	slog.Log(context.Background(), -5, "ReadHeader", "header", header)

	data := make([]byte, length)
	_, err = io.ReadFull(l.c, data)
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	var conn *MuxConn
	l.r.RLock()
	if c, ok := l.accepts[seq]; ok {
		conn = c
	}
	if c, ok := l.dials[seq]; ok {
		conn = c
	}
	l.r.RUnlock()

	if cmd == CMD_DATA {
		if conn == nil {
			conn = &MuxConn{
				fin:     make(chan struct{}),
				finWait: make(chan struct{}),
				inbound: make(chan []byte, 128),
				seq:     seq,
				s:       l,
			}
			l.r.Lock()
			l.accepts[seq] = conn
			l.r.Unlock()
			l.accept <- conn
		}
		conn.inbound <- data
		return nil
	}

	if cmd == CMD_FIN {
		if conn == nil {
			return nil
		}
		close(conn.finWait)
		conn.Close()
		return nil
	}
	return fmt.Errorf("unsupport connmux cmd %d", cmd)
}

func (d *MuxSession) OpenStream() (net.Conn, error) {
	if d.seqGen == nil {
		return nil, errors.New("seq generator must not nil")
	}
	c := &MuxConn{
		fin:     make(chan struct{}),
		finWait: make(chan struct{}),
		inbound: make(chan []byte, 128),
		seq:     d.seqGen.GenSeq(),
		s:       d,
	}
	d.r.Lock()
	d.dials[c.seq] = c
	d.r.Unlock()
	return c, nil
}

func Mux(conn io.ReadWriteCloser, seqGen SeqGen) *MuxSession {
	l := &MuxSession{
		exit:    make(chan struct{}),
		c:       conn,
		seqGen:  seqGen,
		accept:  make(chan net.Conn),
		accepts: make(map[uint32]*MuxConn),
		dials:   make(map[uint32]*MuxConn),
	}
	go l.run()
	return l
}
