package disco

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"time"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/secure"
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
	case CONTROL_CONN:
		return "CONTROL_CONN"
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
	CONTROL_CONN                  ControlCode = 30
)

type Error struct {
	Code int
	Msg  string
}

func (e Error) Wrap(err error) Error {
	return Error{Code: e.Code, Msg: fmt.Sprintf("%s: %s", e.Msg, err)}
}

func (e Error) Error() string {
	return fmt.Sprintf("ENO%d: %s", e.Code, e.Msg)
}

func (e Error) MarshalTo(w io.Writer) {
	json.NewEncoder(w).Encode(e)
}

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
	Internal NATType = "internal"
)

var (
	ErrUseOfClosedConnection error = errors.New("use of closed network connection")
)

var defaultDiscoConfig = DiscoConfig{
	PortScanOffset:            -1000,
	PortScanCount:             3000,
	ChallengesRetry:           5,
	ChallengesInitialInterval: 200 * time.Millisecond,
	ChallengesBackoffRate:     1.65,
}

type DiscoConfig struct {
	PortScanOffset            int
	PortScanCount             int
	ChallengesRetry           int
	ChallengesInitialInterval time.Duration
	ChallengesBackoffRate     float64
}

func SetModifyDiscoConfig(modify func(cfg *DiscoConfig)) {
	if modify != nil {
		modify(&defaultDiscoConfig)
	}
	defaultDiscoConfig.PortScanCount = min(max(32, defaultDiscoConfig.PortScanCount), 65535-1024)
	defaultDiscoConfig.ChallengesRetry = max(1, defaultDiscoConfig.ChallengesRetry)
	defaultDiscoConfig.ChallengesInitialInterval = max(10*time.Millisecond, defaultDiscoConfig.ChallengesInitialInterval)
	defaultDiscoConfig.ChallengesBackoffRate = max(1, defaultDiscoConfig.ChallengesBackoffRate)
}

type Disco struct {
	Magic func() []byte
}

func (d *Disco) NewPing(peerID peer.ID) []byte {
	return slices.Concat(d.magic(), peerID.Bytes())
}

func (d *Disco) ParsePing(b []byte) peer.ID {
	magic := d.magic()
	if len(b) <= len(magic) || len(b) > 255+len(magic) {
		return ""
	}
	if slices.Equal(magic, b[:len(magic)]) {
		return peer.ID(b[len(magic):])
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
	PeerID peer.ID
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
	ID       peer.ID
	Metadata url.Values
}

// PeerUDPAddr describe the peer udp addr
type PeerUDPAddr struct {
	ID   peer.ID
	Addr *net.UDPAddr
	Type NATType
}
