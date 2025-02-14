package aescbc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/sigcn/pg/cache/lru"
	"github.com/sigcn/pg/secure"
)

func PKCS7Padding(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padText...)
}

func PKCS7UnPadding(data []byte) ([]byte, error) {
	length := len(data)
	padding := int(data[length-1])
	if padding < 1 || padding > aes.BlockSize {
		return nil, fmt.Errorf("invalid padding size")
	}

	for i := length - padding; i < length; i++ {
		if data[i] != byte(padding) {
			return nil, fmt.Errorf("invalid padding")
		}
	}
	return data[:len(data)-int(padding)], nil
}

func Encrypt(key, data []byte) ([]byte, error) {
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

func Decrypt(key, data []byte) ([]byte, error) {
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

	return PKCS7UnPadding(data)
}

var _ secure.SymmAlgo = (*AESCBC)(nil)

type AESCBC struct {
	mut              sync.RWMutex
	cipher           *lru.Cache[string, cipher.Block]
	provideSecretKey secure.ProvideSecretKey
}

func (s *AESCBC) Encrypt(b []byte, pubKey string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("aesCBC is nil")
	}
	block, err := s.ensureChiperBlock(pubKey)
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

func (s *AESCBC) Decrypt(b []byte, pubKey string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("aesCBC is nil")
	}
	block, err := s.ensureChiperBlock(pubKey)
	if err != nil {
		return nil, err
	}
	if len(b) < aes.BlockSize || len(b)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted data")
	}

	iv := b[:aes.BlockSize]
	b = b[aes.BlockSize:]

	plainBytes := make([]byte, len(b))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(plainBytes, b)
	return PKCS7UnPadding(plainBytes)
}

func (s *AESCBC) SecretKey() secure.ProvideSecretKey {
	return s.provideSecretKey
}

func (s *AESCBC) ensureChiperBlock(pubKey string) (cipher.Block, error) {
	s.mut.RLock()
	block, ok := s.cipher.Get(pubKey)
	s.mut.RUnlock()
	if !ok {
		secretKey, err := s.provideSecretKey(pubKey)
		if err != nil {
			return nil, err
		}
		b, err := aes.NewCipher(secretKey)
		if err != nil {
			return nil, err
		}
		block = b
		s.mut.Lock()
		s.cipher.Put(pubKey, block)
		s.mut.Unlock()
	}

	return block, nil
}

func New(provideSecretKey secure.ProvideSecretKey) secure.SymmAlgo {
	return &AESCBC{
		cipher:           lru.New[string, cipher.Block](128),
		provideSecretKey: provideSecretKey,
	}
}
