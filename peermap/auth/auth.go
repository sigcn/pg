package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rkonfj/peerguard/secure"
	"storj.io/common/base58"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

type Authenticator interface {
	GenerateToken(string, time.Duration) (string, error)
	VerifyToken(networkIDChiper string) (string, error)
}

type jsonToken struct {
	Network  string `json:"n"`
	Deadline int64  `json:"t"`
}

type authenticator struct {
	key []byte
}

func NewAuthenticator(key string) Authenticator {
	sum := sha256.Sum256([]byte(key))
	return &authenticator{key: sum[:]}
}

func (auth *authenticator) GenerateToken(networkID string, validDuration time.Duration) (string, error) {
	b, err := json.Marshal(jsonToken{
		Network:  networkID,
		Deadline: time.Now().Add(validDuration).Unix(),
	})
	fmt.Println(string(b))
	if err != nil {
		return "", err
	}
	chiperData, err := secure.AESCBCEncrypt(auth.key, b)
	return base58.Encode(chiperData), err
}

func (auth *authenticator) VerifyToken(networkIDChiper string) (string, error) {
	chiperData := base58.Decode(networkIDChiper)
	plainData, err := secure.AESCBCDecrypt(auth.key, chiperData)
	if err != nil {
		return "", ErrInvalidToken
	}

	var token jsonToken
	err = json.Unmarshal(plainData, &token)
	if err != nil {
		return "", ErrInvalidToken
	}

	if time.Until(time.Unix(token.Deadline, 0)) <= 0 {
		return "", ErrTokenExpired
	}
	return token.Network, nil
}
