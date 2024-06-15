package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/rkonfj/peerguard/secure/aescbc"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

type JSONSecret struct {
	Network   string   `json:"n"`
	Alias     string   `json:"n1"`
	Neighbors []string `json:"ns"`
	Deadline  int64    `json:"t"`
}

type Net struct {
	ID        string
	Alias     string
	Neighbors []string
}

type Authenticator struct {
	key []byte
}

func NewAuthenticator(key string) *Authenticator {
	sum := sha256.Sum256([]byte(key))
	return &Authenticator{key: sum[:]}
}

func (auth *Authenticator) GenerateSecret(n Net, validDuration time.Duration) (string, error) {
	b, err := json.Marshal(JSONSecret{
		Network:   n.ID,
		Alias:     n.Alias,
		Neighbors: n.Neighbors,
		Deadline:  time.Now().Add(validDuration).Unix(),
	})
	if err != nil {
		return "", err
	}
	chiperData, err := aescbc.Encrypt(auth.key, b)
	return base64.URLEncoding.EncodeToString(chiperData), err
}

func (auth *Authenticator) ParseSecret(networkIDChiper string) (JSONSecret, error) {
	chiperData, err := base64.URLEncoding.DecodeString(networkIDChiper)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}
	plainData, err := aescbc.Decrypt(auth.key, chiperData)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}

	var token JSONSecret
	err = json.Unmarshal(plainData, &token)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}

	if time.Until(time.Unix(token.Deadline, 0)) <= 0 {
		return token, ErrTokenExpired
	}
	return token, nil
}
