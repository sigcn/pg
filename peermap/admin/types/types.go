package types

import "net/url"

type PeerStore interface {
	Peers(network string) ([]url.Values, error)
}
