package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/lru"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peer/peermap"
	"storj.io/common/base58"
)

type PacketBroadcaster interface {
	Broadcast([]byte) (int, error)
}

var (
	_ net.PacketConn    = (*PeerPacketConn)(nil)
	_ PacketBroadcaster = (*PeerPacketConn)(nil)
)

type PeerPacketConn struct {
	cfg               Config
	closedSig         chan struct{}
	readTimeout       chan struct{}
	udpConn           *disco.UDPConn
	wsConn            *disco.WSConn
	discoCooling      *lru.Cache[peer.ID, time.Time]
	discoCoolingMutex sync.Mutex
}

// ReadFrom reads a packet from the connection,
// copying the payload into p. It returns the number of
// bytes copied into p and the return address that
// was on the packet.
// It returns the number of bytes read (0 <= n <= len(p))
// and any error encountered. Callers should always process
// the n > 0 bytes returned before considering the error err.
// ReadFrom can be made to time out and return an error after a
// fixed time limit; see SetDeadline and SetReadDeadline.
func (c *PeerPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.closedSig:
		err = disco.ErrUseOfClosedConnection
		return
	case <-c.readTimeout:
		err = os.ErrDeadlineExceeded
		return
	case datagram := <-c.wsConn.Datagrams():
		addr = datagram.PeerID
		n = copy(p, datagram.TryDecrypt(c.cfg.SymmAlgo))
		return
	case datagram := <-c.udpConn.Datagrams():
		addr = datagram.PeerID
		n = copy(p, datagram.TryDecrypt(c.cfg.SymmAlgo))
		return
	}
}

// WriteTo writes a packet with payload p to addr.
// WriteTo can be made to time out and return an Error after a
// fixed time limit; see SetDeadline and SetWriteDeadline.
// On packet-oriented connections, write timeouts are rare.
func (c *PeerPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if _, ok := addr.(peer.ID); !ok {
		return 0, errors.New("not a p2p address")
	}

	datagram := disco.Datagram{PeerID: addr.(peer.ID), Data: p}
	p = datagram.TryEncrypt(c.cfg.SymmAlgo)

	n, err = c.udpConn.WriteToUDP(p, datagram.PeerID)
	if err != nil {
		c.TryLeadDisco(datagram.PeerID)
		slog.Log(context.Background(), -3, "[Relay] WriteTo", "addr", datagram.PeerID)
		return len(p), c.wsConn.WriteTo(p, datagram.PeerID, peer.CONTROL_RELAY)
	}
	return
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
func (c *PeerPacketConn) Close() error {
	close(c.closedSig)
	close(c.readTimeout)
	var errs []error
	if err := c.wsConn.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := c.udpConn.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// LocalAddr returns the local network address, if known.
func (c *PeerPacketConn) LocalAddr() net.Addr {
	return c.cfg.PeerID
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
// the deadline after successful ReadFrom or WriteTo calls.
//
// A zero value for t means I/O operations will not time out.
func (c *PeerPacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

// SetReadDeadline sets the deadline for future ReadFrom calls
// and any currently-blocked ReadFrom call.
// A zero value for t means ReadFrom will not time out.
func (c *PeerPacketConn) SetReadDeadline(t time.Time) error {
	timeout := time.Until(t)
	if timeout > 0 {
		time.AfterFunc(timeout, func() { c.readTimeout <- struct{}{} })
	}
	return nil
}

// SetWriteDeadline sets the deadline for future WriteTo calls
// and any currently-blocked WriteTo call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means WriteTo will not time out.
func (c *PeerPacketConn) SetWriteDeadline(t time.Time) error {
	return nil
}

// SetReadBuffer sets the size of the operating system's
// receive buffer associated with the connection.
func (c *PeerPacketConn) SetReadBuffer(bytes int) error {
	return c.udpConn.SetReadBuffer(bytes)
}

// SetWriteBuffer sets the size of the operating system's
// transmit buffer associated with the connection.
func (c *PeerPacketConn) SetWriteBuffer(bytes int) error {
	return c.udpConn.SetWriteBuffer(bytes)
}

// Broadcast broadcast packet to all found peers using direct udpConn
func (c *PeerPacketConn) Broadcast(b []byte) (int, error) {
	return c.udpConn.Broadcast(b)
}

// TryLeadDisco try lead a peer discovery
// disco as soon as every 5 minutes
func (c *PeerPacketConn) TryLeadDisco(peerID peer.ID) {
	if !c.discoCoolingMutex.TryLock() {
		return
	}
	defer c.discoCoolingMutex.Unlock()
	lastTime, ok := c.discoCooling.Get(peerID)
	if !ok || time.Since(lastTime) > 5*time.Minute {
		c.wsConn.LeadDisco(peerID)
		c.discoCooling.Put(peerID, time.Now())
	}
}

// UDPConn return the os udp socket
func (c *PeerPacketConn) UDPConn() net.PacketConn {
	return c.udpConn
}

// ServerStream is the connection stream to the peermap server
func (c *PeerPacketConn) ServerStream() io.ReadWriter {
	return c.wsConn
}

// Peers return the found peers
func (c *PeerPacketConn) Peers() []disco.PeerState {
	return c.udpConn.Peers()
}

// runControlEventLoop events control loop
func (c *PeerPacketConn) runControlEventLoop(wsConn *disco.WSConn, udpConn *disco.UDPConn) {
	for {
		select {
		case peer, ok := <-wsConn.Peers():
			if !ok {
				return
			}
			go udpConn.GenerateLocalAddrsSends(peer.PeerID, wsConn.STUNs())
			if onPeer := c.cfg.OnPeer; onPeer != nil {
				go onPeer(peer.PeerID, peer.Metadata)
			}
		case revcUDPAddr, ok := <-wsConn.PeersUDPAddrs():
			if !ok {
				return
			}
			go udpConn.RunDiscoMessageSendLoop(revcUDPAddr.PeerID, revcUDPAddr.Addr)
		case sendUDPAddr, ok := <-udpConn.UDPAddrSends():
			if !ok {
				return
			}
			go func() {
				for i := 0; i < 3; i++ {
					err := wsConn.WriteTo([]byte(sendUDPAddr.Addr.String()), sendUDPAddr.PeerID, peer.CONTROL_NEW_PEER_UDP_ADDR)
					if err == nil {
						slog.Debug("ListenUDP", "addr", sendUDPAddr.Addr, "for", sendUDPAddr.PeerID)
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
			}()
		case _, ok := <-udpConn.NetworkChangedEvents():
			if !ok {
				return
			}
			go func() {
				c.discoCoolingMutex.Lock()
				defer c.discoCoolingMutex.Unlock()
				c.wsConn.CloseConn()
			}()
		}
	}
}

// SharedKey get the key shared with the peer
func (c *PeerPacketConn) SharedKey(peerID peer.ID) ([]byte, error) {
	if c.cfg.SymmAlgo == nil {
		return nil, errors.New("get shared key from plain conn")
	}
	return c.cfg.SymmAlgo.SecretKey()(peerID.String())
}

// ListenPacket same as ListenPacketContext, but no context required
func ListenPacket(peermap *peermap.Peermap, opts ...Option) (*PeerPacketConn, error) {
	return ListenPacketContext(context.Background(), peermap, opts...)
}

// ListenPacketContext listen the p2p network for read/write packets
func ListenPacketContext(ctx context.Context, peermap *peermap.Peermap, opts ...Option) (*PeerPacketConn, error) {
	id := make([]byte, 16)
	rand.Read(id)
	cfg := Config{
		UDPPort:         29877,
		KeepAlivePeriod: 10 * time.Second,
		PeerID:          peer.ID(base58.Encode(id)),
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("config error: %w", err)
		}
	}

	udpConn, err := disco.ListenUDP(disco.UDPConfig{
		Port:                  cfg.UDPPort,
		DisableIPv4:           cfg.DisableIPv4,
		DisableIPv6:           cfg.DisableIPv6,
		ID:                    cfg.PeerID,
		PeerKeepaliveInterval: cfg.KeepAlivePeriod,
	})
	if err != nil {
		return nil, err
	}

	wsConn, err := disco.DialPeermap(ctx, peermap, cfg.PeerID, cfg.Metadata)
	if err != nil {
		return nil, err
	}

	slog.Info("ListenPeer", "addr", cfg.PeerID)
	packetConn := PeerPacketConn{
		cfg:          cfg,
		closedSig:    make(chan struct{}),
		readTimeout:  make(chan struct{}),
		udpConn:      udpConn,
		wsConn:       wsConn,
		discoCooling: lru.New[peer.ID, time.Time](1024),
	}
	go packetConn.runControlEventLoop(wsConn, udpConn)
	return &packetConn, nil
}
