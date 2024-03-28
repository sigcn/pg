package exporter

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/rkonfj/peerguard/secure"
	"github.com/rkonfj/peerguard/secure/aescbc"
)

var algo secure.SymmAlgo

func SetSecretKey(key string) {
	sum := sha256.Sum256([]byte(key))
	algo = aescbc.New(func(pubKey string) ([]byte, error) {
		return sum[:], nil
	})
}

type Instruction struct {
	ExpiredAt int64 `json:"expired_at"`
}

func CheckToken(token string) (*Instruction, error) {
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	plain, err := algo.Decrypt(b, "")
	if err != nil {
		return nil, err
	}
	var ins Instruction
	json.Unmarshal(plain, &ins)
	if ins.ExpiredAt-time.Now().Unix() <= 0 {
		return nil, errors.New("token expired")
	}
	return &ins, nil
}

func GenerateToken(ins Instruction) (string, error) {
	b, err := json.Marshal(ins)
	if err != nil {
		return "", err
	}
	chiper, err := algo.Encrypt(b, "")
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(chiper), nil
}
