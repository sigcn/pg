package disco

import "strings"

type PeerID string

func (id PeerID) String() string {
	return string(id)
}

func (id PeerID) Network() string {
	return "p2p"
}

func (id PeerID) Len() byte {
	return byte(len(id))
}

func (id PeerID) Bytes() []byte {
	return []byte(id)
}

type Labels []string

func (ls Labels) Get(key string) (string, bool) {
	for _, l := range ls {
		kv := strings.Split(l, "=")
		if len(kv) == 2 {
			if kv[0] == key {
				return kv[1], true
			}
			continue
		}
		if kv[0] == key {
			return "", true
		}
	}
	return "", false
}
