package secure

import (
	"crypto/rand"

	"golang.org/x/crypto/curve25519"
	"storj.io/common/base58"
)

type PrivateKey struct {
	PublicKey
	b []byte
}

func (key *PrivateKey) String() string {
	return base58.Encode(key.b)
}

func (key *PrivateKey) SharedKey(pubKey string) ([]byte, error) {
	b := base58.Decode(pubKey)
	secret, err := curve25519.X25519(key.b, b)
	if err != nil {
		return nil, err
	}
	return secret, nil
}

type PublicKey struct {
	b []byte
}

func (key *PublicKey) String() string {
	return base58.Encode(key.b)
}

func GenerateCurve25519() (*PrivateKey, error) {
	var priv, pub [32]byte
	_, err := rand.Read(priv[:])
	if err != nil {
		return nil, err
	}

	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	curve25519.ScalarBaseMult(&pub, &priv)

	return &PrivateKey{b: priv[:], PublicKey: PublicKey{b: pub[:]}}, nil
}

func Curve25519PrivateKey(privateKey string) (*PrivateKey, error) {
	priv := base58.Decode(privateKey)
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, (*[32]byte)(priv))
	return &PrivateKey{b: priv, PublicKey: PublicKey{b: pub[:]}}, nil
}
