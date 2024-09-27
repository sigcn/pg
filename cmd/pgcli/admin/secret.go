package admin

import (
	"encoding/json"
	"os"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/peermap/auth"
	"github.com/spf13/cobra"
)

func secretCmd() *cobra.Command {
	secretCmd := &cobra.Command{
		Use:   "secret",
		Short: "Generate a pre-shared network secret",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			secretKey, err := requiredArg(cmd.InheritedFlags(), "secret-key")
			if err != nil {
				return err
			}
			network, err := cmd.Flags().GetString("network")
			if err != nil {
				return err
			}
			alias, err := cmd.Flags().GetString("alias")
			if err != nil {
				return err
			}
			validDuration, err := cmd.Flags().GetDuration("duration")
			if err != nil {
				return err
			}
			secret, err := auth.NewAuthenticator(secretKey).GenerateSecret(auth.Net{
				Alias: alias,
				ID:    network,
			}, validDuration)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(disco.NetworkSecret{
				Secret:  secret,
				Network: network,
				Expire:  time.Now().Add(validDuration - 10*time.Second),
			})
		},
	}
	secretCmd.Flags().String("alias", "", "network alias")
	secretCmd.Flags().String("network", "default", "network")
	secretCmd.Flags().Duration("duration", 365*24*time.Hour, "secret duration to expire")

	return secretCmd
}
