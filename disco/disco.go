package disco

import (
	"log/slog"
	"net"
	"net/url"
	"slices"

	"github.com/sigcn/pg/secure"
)

type ControlCode uint8

func (code ControlCode) String() string {
	switch code {
	case CONTROL_RELAY:
		return "RELAY"
	case CONTROL_NEW_PEER:
		return "NEW_PEER"
	case CONTROL_NEW_PEER_UDP_ADDR:
		return "NEW_PEER_UDP_ADDR"
	case CONTROL_LEAD_DISCO:
		return "LEAD_DISCO"
	case CONTROL_UPDATE_NETWORK_SECRET:
		return "UPDATE_NETWORK_SECRET"
	case CONTROL_UPDATE_NAT_INFO:
		return "UPDATE_NAT_INFO"
	case CONTROL_UPDATE_META:
		return "UPDATE_PEER"
	case CONTROL_PEER_LEAVE:
		return "PEER_LEAVE"
	case CONTROL_CONN:
		return "CONTROL_CONN"
	case CONTROL_SERVER_CONNECTED:
		return "SERVER_CONNECTED"
	default:
		return "UNDEFINED"
	}
}

func (code ControlCode) Byte() byte {
	return byte(code)
}

const (
	CONTROL_RELAY                 ControlCode = 0
	CONTROL_NEW_PEER              ControlCode = 1
	CONTROL_NEW_PEER_UDP_ADDR     ControlCode = 2
	CONTROL_LEAD_DISCO            ControlCode = 3
	CONTROL_UPDATE_NETWORK_SECRET ControlCode = 20
	CONTROL_UPDATE_NAT_INFO       ControlCode = 21
	CONTROL_UPDATE_META           ControlCode = 22
	CONTROL_PEER_LEAVE            ControlCode = 25
	CONTROL_CONN                  ControlCode = 30
	CONTROL_SERVER_CONNECTED      ControlCode = 50
)

type NATType string

func (t NATType) AccurateThan(t1 NATType) bool {
	if t == Unknown {
		return false
	}
	if t == t1 {
		return false
	}
	if t == Easy && t1 != Unknown {
		return false
	}
	if t == Hard && !slices.Contains([]NATType{Unknown, Easy}, t1) {
		return false
	}
	return true
}

func (t NATType) Easy() bool {
	return t == Easy || t == EasyIP6
}

func (t NATType) IP4() bool {
	return t == IP4 || t == IP46
}

func (t NATType) String() string {
	if t == "" {
		return "unknown"
	}
	return string(t)
}

const (
	Unknown  NATType = ""
	Hard     NATType = "hard"
	Easy     NATType = "easy"
	UPnP     NATType = "upnp"
	IP4      NATType = "ip4"
	IP6      NATType = "ip6"
	IP46     NATType = "ip4+ip6"
	EasyIP6  NATType = "easy+ip6"
	Internal NATType = "internal"
)

func UDPAddrContains(addrs []*net.UDPAddr, tgt *net.UDPAddr) bool {
	for _, addr := range addrs {
		if addr.IP.Equal(tgt.IP) && addr.Port == tgt.Port {
			return true
		}
	}
	return false
}

type NATInfo struct {
	Type  NATType
	Addrs []*net.UDPAddr
}

func (i *NATInfo) MergeAddrs(addrs []*net.UDPAddr) {
	var toMerge []*net.UDPAddr
	for _, addr := range addrs {
		if UDPAddrContains(i.Addrs, addr) {
			continue
		}
		toMerge = append(toMerge, addr)
	}
	i.Addrs = append(i.Addrs, toMerge...)
}

type Disco struct {
	Magic func() []byte
}

func (d *Disco) NewPing(peerID PeerID) []byte {
	return slices.Concat(d.magic(), peerID.Bytes())
}

func (d *Disco) ParsePing(b []byte) PeerID {
	magic := d.magic()
	if len(b) <= len(magic) || len(b) > 255+len(magic) {
		return ""
	}
	if slices.Equal(magic, b[:len(magic)]) {
		return PeerID(b[len(magic):])
	}
	return ""
}

func (d *Disco) magic() []byte {
	var magic []byte
	if d.Magic != nil {
		magic = d.Magic()
	}
	if len(magic) == 0 {
		return []byte("_ping")
	}
	return magic
}

// Datagram is the packet from peer or to peer
type Datagram struct {
	PeerID PeerID
	Data   []byte
}

// TryDecrypt the datagram from peer
func (d *Datagram) TryDecrypt(symmAlgo secure.SymmAlgo) []byte {
	if symmAlgo == nil {
		return d.Data
	}
	b, err := symmAlgo.Decrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram decrypt error", "err", err)
		return d.Data
	}
	return b
}

// TryEncrypt the datagram to peer
func (d *Datagram) TryEncrypt(symmAlgo secure.SymmAlgo) []byte {
	if symmAlgo == nil {
		return d.Data
	}
	b, err := symmAlgo.Encrypt(d.Data, d.PeerID.String())
	if err != nil {
		slog.Debug("Datagram encrypt error", "err", err)
		return d.Data
	}
	return b
}

// Peer descibe the peer info
type Peer struct {
	ID       PeerID
	Metadata url.Values
}

// PeerUDPAddr describe the peer udp addr
type PeerUDPAddr struct {
	ID   PeerID
	Addr *net.UDPAddr
	Type NATType
}
