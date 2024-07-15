package peer

import (
	"encoding/json"
	"fmt"
	"io"
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

type ID string

func (id ID) String() string {
	return string(id)
}

func (id ID) Network() string {
	return "p2p"
}

func (id ID) Len() byte {
	return byte(len(id))
}

func (id ID) Bytes() []byte {
	return []byte(id)
}

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
