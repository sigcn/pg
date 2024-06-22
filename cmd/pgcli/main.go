package main

import (
	"fmt"
	"log/slog"

	"github.com/rkonfj/peerguard/cmd/pgcli/admin"
	"github.com/rkonfj/peerguard/cmd/pgcli/curve25519"
	"github.com/rkonfj/peerguard/cmd/pgcli/download"
	"github.com/rkonfj/peerguard/cmd/pgcli/share"
	"github.com/rkonfj/peerguard/cmd/pgcli/vpn"
	"github.com/spf13/cobra"
)

var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	cmd := &cobra.Command{
		Use:          "pgcli",
		Version:      fmt.Sprintf("%s, commit %s", Version, Commit),
		Short:        "A p2p network toolset",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			verbose, err := cmd.Flags().GetInt("verbose")
			if err != nil {
				return err
			}
			slog.SetLogLoggerLevel(slog.Level(verbose))
			return nil
		},
	}

	vpn.Version = Version
	vpn.Commit = Commit
	cmd.AddCommand(vpn.Cmd)
	cmd.AddCommand(admin.Cmd)
	cmd.AddCommand(curve25519.Cmd)
	cmd.AddCommand(share.Cmd)
	cmd.AddCommand(download.Cmd)

	cmd.PersistentFlags().IntP("verbose", "V", 0, "logger verbosity level")
	cmd.Execute()
}
