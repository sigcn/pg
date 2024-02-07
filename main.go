package main

import (
	"fmt"
	"log/slog"

	"github.com/rkonfj/peerguard/cmd/curve25519"
	"github.com/rkonfj/peerguard/cmd/serve"
	"github.com/rkonfj/peerguard/cmd/token"
	"github.com/rkonfj/peerguard/cmd/vpn"
	"github.com/spf13/cobra"
)

var (
	Version = "unknown"
	Commit  = "unknown"
)

func main() {
	cmd := &cobra.Command{
		Use:          "peerguard",
		Version:      fmt.Sprintf("%s, commit %s", Version, Commit),
		Short:        "A peer to peer network toolset",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			verbose, err := cmd.Flags().GetInt("v")
			if err != nil {
				return err
			}
			slog.SetLogLoggerLevel(slog.Level(verbose))
			return nil
		},
	}

	cmd.AddCommand(serve.Cmd)
	cmd.AddCommand(token.Cmd)
	cmd.AddCommand(vpn.Cmd)
	cmd.AddCommand(curve25519.Cmd)

	cmd.PersistentFlags().Int("v", 0, "logger verbosity level")

	cmd.Execute()
}
