package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/rkonfj/peerguard/secure"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

type Authenticator interface {
	GenerateSecret(string, time.Duration) (string, error)
	ParseSecret(secret string) (JSONSecret, error)
}

type JSONSecret struct {
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

func (auth *authenticator) GenerateSecret(networkID string, validDuration time.Duration) (string, error) {
	b, err := json.Marshal(JSONSecret{
		Network:  networkID,
		Deadline: time.Now().Add(validDuration).Unix(),
	})
	if err != nil {
		return "", err
	}
	chiperData, err := secure.AESCBCEncrypt(auth.key, b)
	return base64.URLEncoding.EncodeToString(chiperData), err
}

func (auth *authenticator) ParseSecret(networkIDChiper string) (JSONSecret, error) {
	chiperData, err := base64.URLEncoding.DecodeString(networkIDChiper)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}
	plainData, err := secure.AESCBCDecrypt(auth.key, chiperData)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}

	var token JSONSecret
	err = json.Unmarshal(plainData, &token)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}

	if time.Until(time.Unix(token.Deadline, 0)) <= 0 {
		return JSONSecret{}, ErrTokenExpired
	}
	return token, nil
}
