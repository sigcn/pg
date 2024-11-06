package curve25519

import (
	"fmt"

	"github.com/sigcn/pg/secure"
)

func Run() error {
	priv, err := secure.GenerateCurve25519()
	if err != nil {
		return err
	}
	fmt.Printf("priv\t%s\n", priv.String())
	fmt.Printf("pub\t%s\n", priv.PublicKey.String())
	return nil
}
