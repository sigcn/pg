package admin

import (
	"encoding/json"
	"os"

	"github.com/rkonfj/peerguard/peermap/exporter"
	"github.com/spf13/cobra"
)

func peersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peers",
		Short: "Query peers from pgmap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			secretKey, err := requiredArg(cmd.InheritedFlags(), "secret-key")
			if err != nil {
				return err
			}
			server, err := requiredArg(cmd.Flags(), "server")
			if err != nil {
				return err
			}
			c, err := exporter.NewClient(server, secretKey)
			if err != nil {
				return err
			}
			peers, err := c.Peers()
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(peers)
		},
	}

	cmd.Flags().StringP("server", "s", "", "peermap server url")
	return cmd
}
