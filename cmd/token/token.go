package token

import (
	"fmt"
	"time"

	"github.com/rkonfj/peerguard/peermap/auth"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "token",
		Short: "Generate a pre-shared token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusterKey, err := cmd.Flags().GetString("cluster-key")
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
			token, err := auth.NewAuthenticator(clusterKey).GenerateToken(networkID, validDuration)
			if err != nil {
				return err
			}
			fmt.Println(token)
			return nil
		},
	}
	Cmd.Flags().String("network", "", "Network")
	Cmd.Flags().String("cluster-key", "", "Key to generate token")
	Cmd.Flags().Duration("duration", 365*24*time.Hour, "Token valid duration")

	Cmd.MarkFlagRequired("network")
	Cmd.MarkFlagRequired("cluster-key")
}
