package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/sigcn/pg/langs"
)

var (
	ErrInvalidToken = langs.Error{Code: 9000, Msg: "invalid token"}
	ErrTokenExpired = langs.Error{Code: 9001, Msg: "token expired"}
)

const secretVersion byte = 1

type JSONSecret struct {
	Network   string   `json:"n"`
	Admin     bool     `json:"adm,omitzero"`
	Alias     string   `json:"n1,omitzero"`
	Neighbors []string `json:"ns,omitempty"`
	Deadline  int64    `json:"t"`
}

type Net struct {
	ID        string
	Alias     string
	Neighbors []string
}

type Authenticator struct {
	aead cipher.AEAD
}

func NewAuthenticator(key string) *Authenticator {
	sum := sha256.Sum256([]byte(key))
	block := langs.Must(aes.NewCipher(sum[:]))
	return &Authenticator{aead: langs.Must(cipher.NewGCMWithRandomNonce(block))}
}

func (auth *Authenticator) GenerateSecret(n Net, validDuration time.Duration) (string, error) {
	return auth.GenerateSecretAdmin(false, n, validDuration)
}

func (auth *Authenticator) GenerateSecretAdmin(adm bool, n Net, validDuration time.Duration) (string, error) {
	b, err := json.Marshal(JSONSecret{
		Network:   n.ID,
		Admin:     adm,
		Alias:     n.Alias,
		Neighbors: n.Neighbors,
		Deadline:  time.Now().Add(validDuration).Unix(),
	})
	if err != nil {
		return "", err
	}

	version := []byte{secretVersion}
	token := append(version, auth.aead.Seal(nil, nil, b, version)...)
	return base64.URLEncoding.EncodeToString(token), nil
}

func (auth *Authenticator) ParseSecret(networkIDChiper string) (JSONSecret, error) {
	tokenData, err := base64.URLEncoding.DecodeString(networkIDChiper)
	if err != nil {
		return JSONSecret{}, ErrInvalidToken
	}
	if len(tokenData) < 1+auth.aead.Overhead() || tokenData[0] != secretVersion {
		return JSONSecret{}, ErrInvalidToken
	}

	version := tokenData[:1]
	plainData, err := auth.aead.Open(nil, nil, tokenData[1:], version)
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
