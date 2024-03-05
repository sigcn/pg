package token

import (
	"encoding/json"
	"os"
	"time"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/auth"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "token",
		Short: "Generate a pre-shared network secret",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			secretKey, err := cmd.Flags().GetString("secret-key")
			if err != nil {
				return err
			}
			networkID, err := cmd.Flags().GetString("network")
			if err != nil {
				return err
			}
			validDuration, err := cmd.Flags().GetDuration("duration")
			if err != nil {
				return err
			}
			token, err := auth.NewAuthenticator(secretKey).GenerateSecret(networkID, validDuration)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(peer.NetworkSecret{
				Secret:  token,
				Network: networkID,
				Expire:  time.Now().Add(validDuration - 10*time.Second),
			})
		},
	}
	Cmd.Flags().String("network", "", "network")
	Cmd.Flags().String("secret-key", "", "key to generate network secret")
	Cmd.Flags().Duration("duration", 365*24*time.Hour, "secret duration to expire")

	Cmd.MarkFlagRequired("network")
	Cmd.MarkFlagRequired("secret-key")
}
