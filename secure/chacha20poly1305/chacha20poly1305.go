package chacha20poly1305

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/rkonfj/peerguard/lru"
	"github.com/rkonfj/peerguard/secure"
)

var _ secure.SymmAlgo = (*Chacha20Poly1305)(nil)

type Chacha20Poly1305 struct {
	mut              sync.RWMutex
	cipher           *lru.Cache[string, cipher.AEAD]
	provideSecretKey secure.ProvideSecretKey
}

func (s *Chacha20Poly1305) Encrypt(data []byte, pubKey string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("enc is disabled")
	}
	aead, err := s.ensureChiperAEAD(pubKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	binary.LittleEndian.PutUint64(nonce[aead.NonceSize()-8:], uint64(time.Now().Unix()/5))
	return aead.Seal(nil, nonce, data, nil), nil
}

func (s *Chacha20Poly1305) Decrypt(data []byte, pubKey string) ([]byte, error) {
	if s == nil {
		return nil, errors.New("dec is disabled")
	}
	aead, err := s.ensureChiperAEAD(pubKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	startIndex := aead.NonceSize() - 8
	nowUnix := time.Now().Unix()

	binary.LittleEndian.PutUint64(nonce[startIndex:], uint64(nowUnix/5))
	plain, err := aead.Open(nil, nonce, data, nil)
	if err != nil {
		binary.LittleEndian.PutUint64(nonce[startIndex:], uint64(nowUnix/5+1))
		plain, err = aead.Open(nil, nonce, data, nil)
		if err != nil {
			binary.LittleEndian.PutUint64(nonce[startIndex:], uint64(nowUnix/5-1))
			plain, err = aead.Open(nil, nonce, data, nil)
			if err != nil {
				return nil, errors.New("invalid data")
			}
		}
	}
	return plain, nil
}

func (s *Chacha20Poly1305) ensureChiperAEAD(pubKey string) (cipher.AEAD, error) {
	s.mut.RLock()
	aead, ok := s.cipher.Get(pubKey)
	s.mut.RUnlock()
	if !ok {
		secretKey, err := s.provideSecretKey(pubKey)
		if err != nil {
			return nil, err
		}
		b, err := chacha20poly1305.New(secretKey)
		if err != nil {
			return nil, err
		}
		aead = b
		s.mut.Lock()
		s.cipher.Put(pubKey, aead)
		s.mut.Unlock()
	}

	return aead, nil

}

func New(provideSecretKey secure.ProvideSecretKey) *Chacha20Poly1305 {
	return &Chacha20Poly1305{
		cipher:           lru.New[string, cipher.AEAD](128),
		provideSecretKey: provideSecretKey,
	}
}
