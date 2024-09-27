package admin

import (
	"encoding/json"
	"os"

	"github.com/sigcn/pg/peermap/exporter"
	"github.com/spf13/cobra"
)

func networksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "networks",
		Short: "Query networks from pgmap",
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
			networks, err := c.Networks()
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(networks)
		},
	}
	cmd.Flags().StringP("server", "s", "", "peermap server url")
	return cmd
}
