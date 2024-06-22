package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rkonfj/peerguard/peermap"
	"github.com/spf13/cobra"
)

var (
	Version = "unknown"
	Commit  = "unknown"
)

func main() {
	serveCmd := &cobra.Command{
		Use:          "pgmap",
		Version:      fmt.Sprintf("%s, commit %s", Version, Commit),
		Short:        "Run a peermap server daemon",
		SilenceUsage: true,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			verbose, err := cmd.Flags().GetInt("verbose")
			if err != nil {
				return err
			}
			slog.SetLogLoggerLevel(slog.Level(verbose))
			return nil
		},
		Args: cobra.NoArgs,
		RunE: run,
	}
	serveCmd.Flags().StringP("config", "c", "config.yaml", "config file")
	serveCmd.Flags().StringP("listen", "l", "127.0.0.1:9987", "listen http address")
	serveCmd.Flags().String("secret-key", "", "key to generate network secret (defaut generate a random one)")
	serveCmd.Flags().StringSlice("stun", []string{}, "stun server for peers NAT traversal (leave blank to disable NAT traversal)")
	serveCmd.Flags().String("pubnet", "", "public network (leave blank to disable public network)")
	serveCmd.Flags().IntP("verbose", "V", 0, "logger verbosity level")

	serveCmd.Execute()
}

func run(cmd *cobra.Command, args []string) error {
	cfg1, err := commandlineConfig(cmd)
	if err != nil {
		return err
	}

	configFile, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}

	cfg, _ := peermap.ReadConfig(configFile)
	cfg.Overwrite(cfg1)

	srv, err := peermap.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func commandlineConfig(cmd *cobra.Command) (opts peermap.Config, err error) {
	opts.Listen, err = cmd.Flags().GetString("listen")
	if err != nil {
		return
	}
	opts.SecretKey, err = cmd.Flags().GetString("secret-key")
	if err != nil {
		return
	}
	opts.PublicNetwork, err = cmd.Flags().GetString("pubnet")
	if err != nil {
		return
	}
	opts.STUNs, err = cmd.Flags().GetStringSlice("stun")
	return
}
