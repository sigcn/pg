package types

type PeerStore interface {
	Peers(network string) ([]string, error)
}
