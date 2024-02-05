package curve25519

import (
	"fmt"

	"github.com/rkonfj/peerguard/secure"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "curve25519",
		Short: "generate a curve25519 key pair",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			priv, err := secure.GenerateCurve25519()
			if err != nil {
				return err
			}
			fmt.Printf("priv\t%s\n", priv.String())
			fmt.Printf("pub\t%s\n", priv.PublicKey.String())
			return nil
		},
	}
}
