package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/rkonfj/peerguard/secure"
	"github.com/rkonfj/peerguard/secure/aescbc"
)

type Authenticator struct {
	algo secure.SymmAlgo
}

func New(secretKey string) *Authenticator {
	sum := sha256.Sum256([]byte(secretKey))
	return &Authenticator{
		algo: aescbc.New(func(pubKey string) ([]byte, error) {
			return sum[:], nil
		}),
	}
}

type Instruction struct {
	ExpiredAt int64 `json:"expired_at"`
}

func (a *Authenticator) CheckToken(token string) (*Instruction, error) {
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	plain, err := a.algo.Decrypt(b, "")
	if err != nil {
		return nil, err
	}
	if plain[0] != 1 {
		return nil, errors.New("invalid token")
	}
	var ins Instruction
	json.Unmarshal(plain[1:], &ins)
	if ins.ExpiredAt-time.Now().Unix() <= 0 {
		return nil, errors.New("token expired")
	}
	return &ins, nil
}

func (a *Authenticator) GenerateToken(ins Instruction) (string, error) {
	b, err := json.Marshal(ins)
	if err != nil {
		return "", err
	}
	chiper, err := a.algo.Encrypt(append([]byte{1}, b...), "")
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(chiper), nil
}
