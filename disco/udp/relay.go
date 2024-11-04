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

func (proto *relayProtocol) toRelay(p []byte, dst disco.PeerID) []byte {
	pkt := append([]byte(nil), MAGIC_TO_RELAY...)
	pkt = append(pkt, dst.Len())
	pkt = append(pkt, dst.Bytes()...)
	pkt = append(pkt, p...)
	return pkt
}

func (proto *relayProtocol) tryToDst(p []byte, src disco.PeerID) ([]byte, disco.PeerID) {
	if !bytes.Equal(MAGIC_TO_RELAY, p[:4]) {
		return nil, ""
	}
	return proto.toDst(p[5+p[4]:], src), disco.PeerID(p[5 : 5+p[4]])
}

func (proto *relayProtocol) tryRecv(p []byte) ([]byte, disco.PeerID) {
	if !bytes.Equal(MAGIC_FROM_RELAY, p[:4]) {
		return nil, ""
	}
	return p[5+p[4]:], disco.PeerID(p[5 : 5+p[4]])
}

func (proto *relayProtocol) toDst(p []byte, src disco.PeerID) []byte {
	pkt := append([]byte(nil), MAGIC_FROM_RELAY...)
	pkt = append(pkt, src.Len())
	pkt = append(pkt, src.Bytes()...)
	pkt = append(pkt, p...)
	return pkt
}
