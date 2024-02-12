package serve

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rkonfj/peerguard/peermap"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "serve",
	Short: "PeerGuard peermap daemon",
	Args:  cobra.NoArgs,
	RunE:  run,
}

func init() {
	Cmd.Flags().StringP("config", "c", "config.yaml", "config file")
	Cmd.Flags().StringP("listen", "l", "", "listen http address (default 127.0.0.1:9987)")
	Cmd.Flags().String("secret-key", "", "key to generate network secret (defaut generate a random one)")
	Cmd.Flags().StringSlice("stun", []string{}, "stun server for peers NAT traversal (leave blank to disable NAT traversal)")
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
	opts.STUNs, err = cmd.Flags().GetStringSlice("stun")
	return
}
