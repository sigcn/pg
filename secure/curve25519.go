package secure

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

type PrivateKey struct {
	PublicKey
	b []byte
}

func (key *PrivateKey) String() string {
	return base64.URLEncoding.EncodeToString(key.b)
}

func (key *PrivateKey) SharedKey(pubKey string) ([]byte, error) {
	b, err := base64.URLEncoding.DecodeString(pubKey)
	if err != nil {
		return nil, fmt.Errorf("invalid publicKey format: %w", err)
	}
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
	return base64.URLEncoding.EncodeToString(key.b)
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
	priv, err := base64.URLEncoding.DecodeString(privateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid privateKey format: %w", err)
	}
	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, (*[32]byte)(priv))
	return &PrivateKey{b: priv, PublicKey: PublicKey{b: pub[:]}}, nil
}
