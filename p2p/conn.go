package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/disco/tp"
	"github.com/sigcn/pg/lru"
	N "github.com/sigcn/pg/net"
	"github.com/sigcn/pg/netlink"
	"storj.io/common/base58"
)

type PacketBroadcaster interface {
	Broadcast([]byte) (int, error)
}

var (
	_ net.PacketConn    = (*PacketConn)(nil)
	_ PacketBroadcaster = (*PacketConn)(nil)
)

type PacketConn struct {
	cfg               Config
	closedSig         chan struct{}
	udpConn           *tp.UDPConn
	wsConn            *tp.WSConn
	discoCooling      *lru.Cache[disco.PeerID, time.Time]
	discoCoolingMutex sync.Mutex

	deadlineRead N.Deadline
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
func (c *PacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case <-c.closedSig:
		err = net.ErrClosed
		return
	case _, ok := <-c.deadlineRead.Deadline():
		if !ok {
			err = net.ErrClosed
			return
		}
		err = N.ErrDeadline
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
func (c *PacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if _, ok := addr.(disco.PeerID); !ok {
		return 0, errors.New("not a p2p address")
	}

	datagram := disco.Datagram{PeerID: addr.(disco.PeerID), Data: p}
	p = datagram.TryEncrypt(c.cfg.SymmAlgo)

	n, err = c.udpConn.WriteToUDP(p, datagram.PeerID)
	if err != nil {
		if !errors.Is(err, tp.ErrUDPConnInactive) {
			c.TryLeadDisco(datagram.PeerID)
		}
		slog.Log(context.Background(), -3, "[Relay] WriteTo", "addr", datagram.PeerID)
		return len(p), c.wsConn.WriteTo(p, datagram.PeerID, disco.CONTROL_RELAY)
	}
	return
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
func (c *PacketConn) Close() error {
	close(c.closedSig)
	c.deadlineRead.Close()
	var errs []error
	if err := c.wsConn.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := c.udpConn.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// LocalAddr returns the local network address, if known.
func (c *PacketConn) LocalAddr() net.Addr {
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
func (c *PacketConn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}

// SetReadDeadline sets the deadline for future ReadFrom calls
// and any currently-blocked ReadFrom call.
// A zero value for t means ReadFrom will not time out.
func (c *PacketConn) SetReadDeadline(t time.Time) error {
	c.deadlineRead.SetDeadline(t)
	return nil
}

// SetWriteDeadline sets the deadline for future WriteTo calls
// and any currently-blocked WriteTo call.
// Even if write times out, it may return n > 0, indicating that
// some of the data was successfully written.
// A zero value for t means WriteTo will not time out.
func (c *PacketConn) SetWriteDeadline(t time.Time) error {
	return errors.ErrUnsupported
}

// SetReadBuffer sets the size of the operating system's
// receive buffer associated with the connection.
func (c *PacketConn) SetReadBuffer(bytes int) error {
	return c.udpConn.SetReadBuffer(bytes)
}

// SetWriteBuffer sets the size of the operating system's
// transmit buffer associated with the connection.
func (c *PacketConn) SetWriteBuffer(bytes int) error {
	return c.udpConn.SetWriteBuffer(bytes)
}

// Broadcast broadcast packet to all found peers using direct udpConn
func (c *PacketConn) Broadcast(b []byte) (int, error) {
	return c.udpConn.Broadcast(b)
}

// TryLeadDisco try lead a peer discovery
// disco as soon as every minute
func (c *PacketConn) TryLeadDisco(peerID disco.PeerID) {
	if !c.discoCoolingMutex.TryLock() {
		return
	}
	defer c.discoCoolingMutex.Unlock()
	lastTime, ok := c.discoCooling.Get(peerID)
	if !ok || time.Since(lastTime) > c.cfg.MinDiscoPeriod {
		c.wsConn.LeadDisco(peerID)
		c.discoCooling.Put(peerID, time.Now())
	}
}

// ServerStream is the connection stream to the peermap server
func (c *PacketConn) ServerStream() io.ReadWriter {
	return c.wsConn
}

// ServerURL is the connected peermap server url
func (c *PacketConn) ServerURL() string {
	return c.wsConn.ServerURL()
}

// ControllerManager makes changes attempting to move the current state towards the desired state
func (c *PacketConn) ControllerManager() disco.ControllerManager {
	return c.wsConn
}

// PeerStore stores the found peers
func (c *PacketConn) PeerStore() tp.PeerStore {
	return c.udpConn
}

// SharedKey get the key shared with the peer
func (c *PacketConn) SharedKey(peerID disco.PeerID) ([]byte, error) {
	if c.cfg.SymmAlgo == nil {
		return nil, errors.New("get shared key from plain conn")
	}
	return c.cfg.SymmAlgo.SecretKey()(peerID.String())
}

// runNetworkChangeDetectLoop listen network change and restart udp and websocket listener
func (c *PacketConn) runNetworkChangeDetectLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan netlink.AddrUpdate)
	if err := netlink.AddrSubscribe(ctx, ch); err != nil {
		close(ch)
		slog.Error("AddrUpdateEventLoop", "err", err)
		return
	}

	go func() {
		<-c.closedSig
		cancel()
	}()

	foundIPMap := map[string]struct{}{}
	ips, _ := disco.ListLocalIPs()
	for _, ip := range ips {
		foundIPMap[ip.String()] = struct{}{}
	}

	for e := range ch {
		if e.Addr.IP.IsLinkLocalUnicast() {
			continue
		}
		if !e.New {
			delete(foundIPMap, e.Addr.IP.String())
			continue
		}
		if disco.IPIgnored(e.Addr.IP) {
			continue
		}
		if _, ok := foundIPMap[e.Addr.IP.String()]; ok {
			continue
		}
		foundIPMap[e.Addr.IP.String()] = struct{}{}

		slog.Log(context.Background(), -2, "NewAddr", "addr", e.Addr.String(), "link", e.LinkIndex)
		if err := c.udpConn.RestartListener(); err != nil {
			slog.Error("RestartUDPListener", "err", err)
		}

		c.udpConn.DetectNAT(c.wsConn.STUNs()) // update NAT type

		if err := c.wsConn.RestartListener(); err != nil {
			slog.Error("RestartWebsocketListener", "err", err)
		}
		c.discoCoolingMutex.Lock()
		c.discoCooling.Clear()
		c.discoCoolingMutex.Unlock()
	}
}

// runControlEventLoop events control loop
func (c *PacketConn) runControlEventLoop() {
	for {
		select {
		case peer, ok := <-c.wsConn.Peers():
			if !ok {
				return
			}
			go c.udpConn.GenerateLocalAddrsSends(peer.ID, c.wsConn.STUNs())
			if onPeer := c.cfg.OnPeer; onPeer != nil {
				go onPeer(peer.ID, peer.Metadata)
			}
		case natEvent, ok := <-c.udpConn.NATEvents():
			if !ok {
				return
			}
			go c.wsConn.UpdateNATInfo(*natEvent)
		case revcUDPAddr, ok := <-c.wsConn.PeersUDPAddrs():
			if !ok {
				return
			}
			go c.udpConn.RunDiscoMessageSendLoop(*revcUDPAddr)
		case sendUDPAddr, ok := <-c.udpConn.UDPAddrSends():
			if !ok {
				return
			}
			go func() {
				for i := 0; i < 3; i++ {
					data := []byte{'a'}
					addr := []byte(sendUDPAddr.Addr.String())
					data = append(data, byte(len(addr)))
					data = append(data, addr...)
					data = append(data, []byte(sendUDPAddr.Type)...)
					err := c.wsConn.WriteTo(data, sendUDPAddr.ID, disco.CONTROL_NEW_PEER_UDP_ADDR)
					if err == nil {
						slog.Debug("ListenUDP", "addr", sendUDPAddr.Addr, "for", sendUDPAddr.ID)
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
			}()
		}
	}
}

// ListenPacket same as ListenPacketContext, but no context required
func ListenPacket(peermap *disco.Peermap, opts ...Option) (*PacketConn, error) {
	return ListenPacketContext(context.Background(), peermap, opts...)
}

// ListenPacketContext listen the p2p network for read/write packets
func ListenPacketContext(ctx context.Context, peermap *disco.Peermap, opts ...Option) (*PacketConn, error) {
	id := make([]byte, 16)
	rand.Read(id)
	cfg := Config{
		UDPPort:         29877,
		KeepAlivePeriod: 10 * time.Second,
		PeerID:          disco.PeerID(base58.Encode(id)),
		MinDiscoPeriod:  2 * time.Minute,
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("config error: %w", err)
		}
	}

	udpConn, err := tp.ListenUDP(tp.UDPConfig{
		Port:                  cfg.UDPPort,
		DisableIPv4:           cfg.DisableIPv4,
		DisableIPv6:           cfg.DisableIPv6,
		ID:                    cfg.PeerID,
		PeerKeepaliveInterval: cfg.KeepAlivePeriod,
	})
	if err != nil {
		return nil, err
	}

	wsConn, err := tp.DialPeermap(ctx, peermap, cfg.PeerID, cfg.Metadata)
	if err != nil {
		udpConn.Close()
		return nil, err
	}

	udpConn.DetectNAT(wsConn.STUNs())

	slog.Info("ListenPeer", "addr", cfg.PeerID)
	packetConn := PacketConn{
		cfg:          cfg,
		closedSig:    make(chan struct{}),
		udpConn:      udpConn,
		wsConn:       wsConn,
		discoCooling: lru.New[disco.PeerID, time.Time](1024),
	}
	go packetConn.runControlEventLoop()
	go packetConn.runNetworkChangeDetectLoop()
	return &packetConn, nil
}
