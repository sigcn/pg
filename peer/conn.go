package peer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/gorilla/websocket"
	cmap "github.com/orcaman/concurrent-map/v2"
	"github.com/rkonfj/peerguard/peernet"
	"tailscale.com/net/stun"
)

type stunBindContext struct {
	peerID peernet.PeerID
	ctime  time.Time
}

type PeerPacketConn struct {
	node          *Node
	udpPacketConn *net.UDPConn
	peerID        peernet.PeerID
	wsConn        *websocket.Conn
	nonce         byte
	inbound       chan []byte
	ctx           context.Context
	ctxCancel     context.CancelFunc
	stunTxIDMap   cmap.ConcurrentMap[string, stunBindContext]
	stunChan      chan []byte
	stunServers   []string
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
	case <-c.ctx.Done():
		err = io.ErrClosedPipe
		return
	case <-c.node.closedSig:
		err = io.ErrClosedPipe
		c.Close()
		return
	default:
	}
	b := <-c.inbound
	addr = peernet.PeerID(b[2 : b[1]+2])
	n = copy(p, b[b[1]+2:])
	return
}

// WriteTo writes a packet with payload p to addr.
// WriteTo can be made to time out and return an Error after a
// fixed time limit; see SetDeadline and SetWriteDeadline.
// On packet-oriented connections, write timeouts are rare.
func (c *PeerPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if _, ok := addr.(peernet.PeerID); !ok {
		return 0, errors.New("not a p2p address")
	}
	tgtPeer := addr.(peernet.PeerID)
	if tgtUDPAddr := c.node.PeerUDPAddr(tgtPeer); tgtUDPAddr != nil {
		return c.udpPacketConn.WriteTo(p, c.node.PeerUDPAddr(tgtPeer))
	}
	return len(p), c.writeTo(p, tgtPeer, 0)
}

func (c *PeerPacketConn) writeTo(p []byte, tgtPeer peernet.PeerID, action byte) error {
	b := make([]byte, 0, 2+len(tgtPeer)+len(p))
	b = append(b, action)             // relay
	b = append(b, tgtPeer.Len())      // addr length
	b = append(b, tgtPeer.Bytes()...) // addr
	b = append(b, p...)               // data
	for i, v := range b {
		b[i] = v ^ c.nonce
	}
	return c.wsConn.WriteMessage(websocket.BinaryMessage, b)
}

// Close closes the connection.
// Any blocked ReadFrom or WriteTo operations will be unblocked and return errors.
func (c *PeerPacketConn) Close() error {
	c.ctxCancel()
	close(c.stunChan)
	close(c.inbound)
	c.wsConn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	return c.wsConn.Close()
}

// LocalAddr returns the local network address, if known.
func (c *PeerPacketConn) LocalAddr() net.Addr {
	return c.peerID
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
	return nil
}

// SetReadDeadline sets the deadline for future ReadFrom calls
// and any currently-blocked ReadFrom call.
// A zero value for t means ReadFrom will not time out.
func (c *PeerPacketConn) SetReadDeadline(t time.Time) error {
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

func (c *PeerPacketConn) runReadLoop() {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		slog.Error("listen udp error", "err", err)
	}
	c.udpPacketConn = udpConn
	go c.runReadUDPLoop()
	go c.runNATTraversalLoop()
	for {
		mt, b, err := c.wsConn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error(err.Error())
			}
			c.wsConn.Close()
			return
		}
		switch mt {
		case websocket.PingMessage:
			c.wsConn.WriteMessage(websocket.PongMessage, nil)
			continue
		case websocket.BinaryMessage:
		default:
			continue
		}
		for i, v := range b {
			b[i] = v ^ c.nonce
		}
		switch b[0] {
		case peernet.CONTROL_RELAY:
			c.inbound <- b
		case peernet.CONTROL_PRE_NAT_TRAVERSAL:
			c.startNATTraversalEngine(b)
		case peernet.CONTROL_NAT_TRAVERSAL:
			c.natTraversal(b)
		}
	}
}

func (c *PeerPacketConn) runReadUDPLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		n, peerAddr, err := c.udpPacketConn.ReadFromUDP(buf)
		if err != nil {
			slog.Error("read from udp error", "err", err)
			break
		}
		// ping
		if n > 4 && string(buf[:5]) == "_ping" && n <= 259 {
			peerID := string(buf[5:n])
			updated := c.node.UpdatePeer(peernet.PeerID(peerID), peerAddr)
			if updated {
				slog.Info("[udp] add peer", "id", peerID, "addr", peerAddr)
			}
			continue
		}
		// stun
		if stun.Is(buf[:n]) {
			b := make([]byte, n)
			copy(b, buf[:n])
			c.stunChan <- b
			continue
		}

		peerID := c.node.PeerID(peerAddr)
		bb := make([]byte, 2+len(peerID)+n)
		bb[1] = peerID.Len()
		copy(bb[2:], peerID.Bytes())
		copy(bb[2+len(peerID):], buf[:n])
		c.inbound <- bb
	}
}

func (c *PeerPacketConn) startNATTraversalEngine(b []byte) {
	go func() {
		peerID := peernet.PeerID(b[2 : b[1]+2])
		json.Unmarshal(b[b[1]+2:], &c.stunServers)
		for _, stunServer := range c.stunServers {
			uaddr, err := net.ResolveUDPAddr("udp", stunServer)
			if err != nil {
				slog.Error(err.Error())
				continue
			}

			txID := stun.NewTxID()
			c.stunTxIDMap.Set(string(txID[:]), stunBindContext{peerID: peerID, ctime: time.Now()})
			req := stun.Request(txID)
			_, err = c.udpPacketConn.WriteToUDP(req, uaddr)
			if err != nil {
				slog.Error(err.Error())
				continue
			}
			time.Sleep(3 * time.Second)
			if c.node.PeerUDPAddr(peerID) != nil {
				break
			}
		}
	}()
}

func (c *PeerPacketConn) runNATTraversalLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		stunResp := <-c.stunChan

		tid, saddr, err := stun.ParseResponse(stunResp)
		if err != nil {
			slog.Error(err.Error())
			continue
		}

		stunBindCtx, ok := c.stunTxIDMap.Get(string(tid[:]))
		if !ok {
			slog.Error("txid mismatch", "got", tid)
			continue
		}

		if !saddr.IsValid() {
			continue
		}
		for i := 0; i < 3; i++ {
			err := c.writeTo([]byte(saddr.String()), stunBindCtx.peerID, peernet.CONTROL_NAT_TRAVERSAL)
			if err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		c.stunTxIDMap.Remove(string(tid[:]))
	}
}

func (c *PeerPacketConn) natTraversal(b []byte) {
	targetUDPAddr := b[b[1]+2:]
	udpAddr, err := net.ResolveUDPAddr("udp", string(targetUDPAddr))
	if err != nil {
		slog.Error("resolve udp addr error", "err", err)
		return
	}
	go func() {
		interval := 200 * time.Millisecond
		for {
			select {
			case <-c.ctx.Done():
				return
			default:
			}
			if len(c.node.PeerID(udpAddr)) > 0 {
				interval = 10 * time.Second
			}
			slog.Debug("Sending ping", "peer", c.node.PeerID(udpAddr), "addr", udpAddr)
			c.udpPacketConn.WriteToUDP([]byte("_ping"+c.peerID), udpAddr)
			time.Sleep(interval)
		}
	}()
}
