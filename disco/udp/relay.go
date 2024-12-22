package udp

import (
	"bytes"

	"github.com/sigcn/pg/disco"
)

var (
	MAGIC_TO_RELAY   = []byte{'_', 'p', 'g', 1}
	MAGIC_FROM_RELAY = []byte{'_', 'p', 'g', 3}
)

type relayProtocol struct {
}

// toRelay make a udp packet later send to relay
func (proto *relayProtocol) toRelay(p []byte, dst disco.PeerID) []byte {
	pkt := append([]byte(nil), MAGIC_TO_RELAY...)
	pkt = append(pkt, dst.Len())
	pkt = append(pkt, dst.Bytes()...)
	pkt = append(pkt, p...)
	return pkt
}

// tryToDst decode udp packet and make a udp packet later send to dest if it is a relay_to packet
func (proto *relayProtocol) tryToDst(p []byte, src disco.PeerID) ([]byte, disco.PeerID) {
	if !bytes.Equal(MAGIC_TO_RELAY, p[:4]) {
		return nil, ""
	}
	return proto.toDst(p[5+p[4]:], src), disco.PeerID(p[5 : 5+p[4]])
}

// tryRecv decode udp packet and return datagram if it is a relay_from packet
func (proto *relayProtocol) tryRecv(p []byte) ([]byte, disco.PeerID) {
	if !bytes.Equal(MAGIC_FROM_RELAY, p[:4]) {
		return nil, ""
	}
	return append([]byte(nil), p[5+p[4]:]...), disco.PeerID(p[5 : 5+p[4]])
}

// toDst make a udp packet later send to dest
func (proto *relayProtocol) toDst(p []byte, src disco.PeerID) []byte {
	pkt := append([]byte(nil), MAGIC_FROM_RELAY...)
	pkt = append(pkt, src.Len())
	pkt = append(pkt, src.Bytes()...)
	pkt = append(pkt, p...)
	return pkt
}
