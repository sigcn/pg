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

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "serve",
		Short: "PeerGuard peermap daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
		},
	}
	Cmd.Flags().StringP("config", "c", "config.yaml", "config file")
	Cmd.Flags().StringP("listen", "l", "", "listen http address")
	Cmd.Flags().String("advertise-url", "", "advertised url for this peermap server (default: auto-detect)")
	Cmd.Flags().String("cluster-key", "", "key to generate network secret (cluster nodes must use the same shared key)")
	Cmd.Flags().StringSlice("stun", []string{}, "stun server for peers NAT traversal (leave blank to disable NAT traversal)")

	Cmd.MarkFlagRequired("cluster-key")
}

func commandlineConfig(cmd *cobra.Command) (opts peermap.Config, err error) {
	opts.Listen, err = cmd.Flags().GetString("listen")
	if err != nil {
		return
	}
	opts.ClusterKey, err = cmd.Flags().GetString("cluster-key")
	if err != nil {
		return
	}
	opts.STUNs, err = cmd.Flags().GetStringSlice("stun")
	if err != nil {
		return
	}
	opts.AdvertiseURL, err = cmd.Flags().GetString("advertise-url")
	return
}
