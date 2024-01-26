package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/rkonfj/peerguard/cmd/chat"
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
			verbose, err := cmd.Flags().GetInt("verbose")
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level: slog.Level(verbose),
			}))
			slog.SetDefault(logger)
			return nil
		},
	}

	cmd.AddCommand(serve.Cmd)
	cmd.AddCommand(token.Cmd)
	cmd.AddCommand(chat.Cmd)
	cmd.AddCommand(vpn.Cmd)

	cmd.PersistentFlags().IntP("verbose", "v", 0, "logger verbosity level")

	cmd.Execute()
}
