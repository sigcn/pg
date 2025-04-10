package curve25519

import (
	"crypto/ecdh"
	"crypto/rand"
	"fmt"

	"storj.io/common/base58"
)

func Run() error {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Printf("priv\t%s\n", base58.Encode(priv.Bytes()))
	fmt.Printf("pub\t%s\n", base58.Encode(priv.PublicKey().Bytes()))
	return nil
}
