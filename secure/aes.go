package secure

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/rkonfj/peerguard/lru"
	"github.com/rkonfj/peerguard/peer"
)

func PKCS7Padding(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func PKCS7UnPadding(data []byte) []byte {
	padding := data[len(data)-1]
	return data[:len(data)-int(padding)]
}

func AESCBCEncrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	dataBytes := PKCS7Padding([]byte(data), aes.BlockSize)

	cipherText := make([]byte, aes.BlockSize+len(dataBytes))
	iv := cipherText[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], dataBytes)

	return cipherText, nil
}

func AESCBCDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(data) < aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted data")
	}

	iv := data[:aes.BlockSize]
	data = data[aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(data, data)

	decryptedData := PKCS7UnPadding(data)
	return decryptedData, nil
}

type AESCBC struct {
	mut    sync.RWMutex
	cipher *lru.Cache[peer.PeerID, cipher.Block]
	priv   *PrivateKey
}

func NewAESCBC(priv *PrivateKey) *AESCBC {
	return &AESCBC{
		cipher: lru.New[peer.PeerID, cipher.Block](128),
		priv:   priv,
	}
}

func (s *AESCBC) Encrypt(b []byte, peerID peer.PeerID) ([]byte, error) {
	if s == nil {
		return nil, errors.New("aesCBC is nil")
	}
	block, err := s.ensureChiperBlock(peerID)
	if err != nil {
		return nil, err
	}
	dataBytes := PKCS7Padding([]byte(b), aes.BlockSize)

	cipherText := make([]byte, aes.BlockSize+len(dataBytes))
	iv := cipherText[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], dataBytes)
	return cipherText, nil
}

func (s *AESCBC) Decrypt(b []byte, peerID peer.PeerID) ([]byte, error) {
	if s == nil {
		return nil, errors.New("aesCBC is nil")
	}
	block, err := s.ensureChiperBlock(peerID)
	if err != nil {
		return nil, err
	}
	if len(b) < aes.BlockSize || len(b)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted data")
	}

	iv := b[:aes.BlockSize]
	b = b[aes.BlockSize:]

	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(b, b)

	return PKCS7UnPadding(b), nil
}

func (s *AESCBC) ensureChiperBlock(peerID peer.PeerID) (cipher.Block, error) {
	s.mut.RLock()
	block, ok := s.cipher.Get(peerID)
	s.mut.RUnlock()
	if !ok {
		secretKey, err := s.priv.SharedKey(peerID.String())
		if err != nil {
			return nil, err
		}
		b, err := aes.NewCipher(secretKey)
		if err != nil {
			return nil, err
		}
		block = b
		s.mut.Lock()
		s.cipher.Put(peerID, block)
		s.mut.Unlock()
	}

	return block, nil
}
