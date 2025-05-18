package p2p

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sigcn/pg/cache"
	"github.com/sigcn/pg/cache/lru"
	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/disco/udp"
	"github.com/sigcn/pg/disco/ws"
	N "github.com/sigcn/pg/net"
	"github.com/sigcn/pg/netlink"
	"storj.io/common/base58"
)

var (
	_ net.PacketConn = (*PacketConn)(nil)

	ErrNoRelayPeer = errors.New("no relay peer")
)

type NodeInfo struct {
	ID      disco.PeerID  `json:"id"`
	Meta    url.Values    `json:"meta"`
	NATInfo disco.NATInfo `json:"nat"`
}

type PacketConn struct {
	cfg               Config
	closeChan         chan struct{}
	closeOnce         sync.Once
	udpConn           *udp.UDPConn
	wsConn            *ws.WSConn
	peerMap           *lru.Cache[disco.PeerID, url.Values]
	peerMapMutex      sync.RWMutex
	discoCooling      *lru.Cache[disco.PeerID, time.Time]
	discoCoolingMutex sync.Mutex
	transportMode     TransportMode

	deadlineRead N.Deadline

	relayPeerIndex atomic.Uint64
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
	case <-c.closeChan:
		err = net.ErrClosed
		return
	case _, ok := <-c.deadlineRead.Deadline():
		if !ok {
			err = net.ErrClosed
			return
		}
		err = N.ErrDeadline
		return
	case datagram, ok := <-c.wsConn.Datagrams():
		if !ok {
			err = net.ErrClosed
			return
		}
		addr = datagram.PeerID
		n = copy(p, datagram.TryDecrypt(c.cfg.SymmAlgo))
		return
	case datagram, ok := <-c.udpConn.Datagrams():
		if !ok {
			err = net.ErrClosed
			return
		}
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

	select {
	case <-c.closeChan:
		err = net.ErrClosed
		return
	default:
	}

	datagram := disco.Datagram{PeerID: addr.(disco.PeerID), Data: p}
	p = datagram.TryEncrypt(c.cfg.SymmAlgo)

	if c.transportMode == MODE_FORCE_RELAY {
		return len(p), c.wsConn.WriteTo(p, datagram.PeerID, disco.CONTROL_RELAY)
	}

	if c.transportMode == MODE_FORCE_PEER_RELAY {
		relay := c.relayPeer(datagram.PeerID)
		if relay == "" {
			return 0, ErrNoRelayPeer
		}
		return c.udpConn.RelayTo(relay, p, datagram.PeerID)
	}

	if n, err = c.udpConn.WriteTo(p, datagram.PeerID); err == nil {
		return
	}

	if !errors.Is(err, udp.ErrUDPConnInactive) {
		c.TryLeadDisco(datagram.PeerID)
	}

	if relay := c.relayPeer(datagram.PeerID); relay != "" {
		if n, err = c.udpConn.RelayTo(relay, p, datagram.PeerID); err == nil {
			return
		}
	}

	return len(p), c.wsConn.WriteTo(p, datagram.PeerID, disco.CONTROL_RELAY)
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
func (c *PacketConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeChan)
		c.deadlineRead.Close()
		c.udpConn.Close()
		c.wsConn.Close()
	})
	return nil
}

// LocalAddr returns the local network address, if known.
func (c *PacketConn) LocalAddr() net.Addr {
	return c.cfg.PeerInfo.ID
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

// SetTransportMode sets func WriteTo underlying transport mode
// p2p.MODE_DEFAULT            p2p > peer_relay > server_relay
// p2p.MODE_FORCE_PEER_RELAY   force to peer_relay
// p2p.MODE_FORCE_RELAY        force to server_relay
func (c *PacketConn) SetTransportMode(mode TransportMode) {
	c.transportMode = mode
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
func (c *PacketConn) PeerStore() udp.PeerStore {
	return c.udpConn
}

// SharedKey get the key shared with the peer
func (c *PacketConn) SharedKey(peerID disco.PeerID) ([]byte, error) {
	if c.cfg.SymmAlgo == nil {
		return nil, errors.New("get shared key from plain conn")
	}
	return c.cfg.SymmAlgo.SecretKey()(peerID.String())
}

// PeerMeta find peer metadata from all found peers
func (c *PacketConn) PeerMeta(peerID disco.PeerID) url.Values {
	c.peerMapMutex.RLock()
	defer c.peerMapMutex.RUnlock()
	if meta, ok := c.peerMap.Get(peerID); ok {
		return meta
	}
	return nil
}

// NodeInfo get information about this node
func (c *PacketConn) NodeInfo() NodeInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	natInfo := c.udpConn.DetectNAT(ctx, c.wsConn.STUNs())
	return NodeInfo{
		ID:      c.cfg.PeerInfo.ID,
		Meta:    c.cfg.PeerInfo.Metadata,
		NATInfo: natInfo,
	}
}

// relayPeer find the suitable relay peer
func (c *PacketConn) relayPeer(peerID disco.PeerID) disco.PeerID {
	selectRelayPeer := func(_ string) disco.PeerID {
		peers := c.PeerStore().Peers()
		for range len(peers) {
			index := c.relayPeerIndex.Add(1) % uint64(len(peers))
			p := peers[index]
			if p.PeerID == peerID {
				continue
			}
			meta := c.PeerMeta(p.PeerID)
			if meta == nil {
				continue
			}
			if _, ok := disco.Labels(meta["label"]).Get("node.nr"); ok {
				// can not as relay peer when `node.nr` label is present
				continue
			}
			peerNAT := disco.NATType(meta.Get("nat"))
			if peerNAT.Easy() || peerNAT.IP4() {
				return p.PeerID
			}
		}
		return ""
	}
	return cache.LoadTTL(peerID.String(), time.Millisecond, selectRelayPeer)
}

// networkChangeDetect listen network change and restart udp and websocket listener
func (c *PacketConn) networkChangeDetect() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan netlink.AddrUpdate)
	if err := netlink.AddrSubscribe(ctx, ch); err != nil {
		close(ch)
		slog.Error("AddrUpdateEventLoop", "err", err)
		return
	}

	go func() {
		<-c.closeChan
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
		if disco.IsIgnoredLocalIP(e.Addr.IP) {
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

		c.udpConn.DetectNAT(context.TODO(), c.wsConn.STUNs()) // update NAT type

		if err := c.wsConn.RestartListener(); err != nil {
			slog.Error("RestartWebsocketListener", "err", err)
		}
		c.discoCoolingMutex.Lock()
		c.discoCooling.Clear()
		c.discoCoolingMutex.Unlock()
	}
}

// eventsHandle events control loop
func (c *PacketConn) eventsHandle() {
	// handle control event
	handleEvent := func(e ws.Event) {
		switch e.ControlCode {
		case disco.CONTROL_NEW_PEER:
			peer := e.Data.(*disco.Peer)
			c.udpConn.GenerateLocalAddrsSends(peer.ID, c.wsConn.STUNs())
			c.peerMapMutex.Lock()
			c.peerMap.Put(peer.ID, peer.Metadata)
			c.peerMapMutex.Unlock()
			if onPeer := c.cfg.OnPeer; onPeer != nil {
				go onPeer(peer.ID, peer.Metadata)
			}
		case disco.CONTROL_PEER_LEAVE:
			if onLeave := c.cfg.OnPeerLeave; onLeave != nil {
				go onLeave(e.Data.(disco.PeerID))
			}
		case disco.CONTROL_UPDATE_META:
			peer := e.Data.(*disco.Peer)
			c.peerMapMutex.Lock()
			c.peerMap.Put(peer.ID, peer.Metadata)
			c.peerMapMutex.Unlock()
			if onPeer := c.cfg.OnPeer; onPeer != nil {
				onPeer(peer.ID, peer.Metadata)
			}
		case disco.CONTROL_NEW_PEER_UDP_ADDR:
			c.udpConn.RunDiscoMessageSendLoop(e.Data.(disco.Endpoint))
		case disco.CONTROL_SERVER_CONNECTED:
			go c.udpConn.DetectNAT(context.Background(), c.wsConn.STUNs())
		}
	}

	// send my udp addr to peer
	sendEndpoint := func(sendUDPAddr *disco.Endpoint) {
		data := []byte{'a'}
		addr := []byte(sendUDPAddr.Addr.String())
		data = append(data, byte(len(addr)))
		data = append(data, addr...)
		data = append(data, []byte(sendUDPAddr.Type)...)
		logger := slog.With("addr", sendUDPAddr.Addr, "peer", sendUDPAddr.ID)
		if err := c.wsConn.WriteTo(data, sendUDPAddr.ID, disco.CONTROL_NEW_PEER_UDP_ADDR); err != nil {
			logger.Warn("UDPAddrSend", "err", err)
			return
		}
		if meta := c.PeerMeta(sendUDPAddr.ID); meta != nil && meta.Get("alias1") != "" {
			logger.Debug("UDPAddrSend", "alias1", meta.Get("alias1"))
		} else {
			logger.Debug("UDPAddrSend")
		}
	}

	for {
		select {
		case <-c.closeChan:
			return
		case e, ok := <-c.wsConn.Events():
			if !ok {
				return
			}
			go handleEvent(e)
		case natEvent, ok := <-c.udpConn.NATEvents():
			if !ok {
				return
			}
			go c.wsConn.UpdateNATInfo(*natEvent)
		case endpoint, ok := <-c.udpConn.Endpoints():
			if !ok {
				return
			}
			go sendEndpoint(endpoint)
		}
	}
}

// ListenPacket same as ListenPacketContext, but no context required
func ListenPacket(peermap *disco.Server, opts ...Option) (*PacketConn, error) {
	return ListenPacketContext(context.Background(), peermap, opts...)
}

// ListenPacketContext listen the p2p network for read/write packets
func ListenPacketContext(ctx context.Context, server *disco.Server, opts ...Option) (*PacketConn, error) {
	id := make([]byte, 16)
	rand.Read(id)
	cfg := Config{
		UDPPort:         29877,
		KeepAlivePeriod: 10 * time.Second,
		PeerInfo:        disco.Peer{ID: disco.PeerID(base58.Encode(id))},
		MinDiscoPeriod:  2 * time.Minute,
	}
	for _, opt := range opts {
		if err := opt(&cfg); err != nil {
			return nil, fmt.Errorf("config error: %w", err)
		}
	}

	udpConn, err := udp.ListenUDP(udp.UDPConfig{
		Port:                  cfg.UDPPort,
		DisableIPv4:           cfg.DisableIPv4,
		DisableIPv6:           cfg.DisableIPv6,
		ID:                    cfg.PeerInfo.ID,
		PeerKeepaliveInterval: cfg.KeepAlivePeriod,
	})
	if err != nil {
		return nil, err
	}

	wsConn, err := ws.Dial(ctx, &cfg.PeerInfo, server)
	if err != nil {
		udpConn.Close()
		return nil, err
	}

	slog.Info("ListenPeer", "addr", cfg.PeerInfo.ID)
	pc := PacketConn{
		cfg:          cfg,
		closeChan:    make(chan struct{}),
		udpConn:      udpConn,
		wsConn:       wsConn,
		peerMap:      lru.New[disco.PeerID, url.Values](1024),
		discoCooling: lru.New[disco.PeerID, time.Time](1024),
	}
	go pc.eventsHandle()
	go pc.networkChangeDetect()
	return &pc, nil
}
