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
			opts, err := processOptions(cmd)
			if err != nil {
				return err
			}
			srv := peermap.New(opts)
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			if err := srv.Serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	Cmd.Flags().StringP("listen", "l", "127.0.0.1:9987", "listen address for this peermap server")
	Cmd.Flags().String("advertise-url", "", "advertised url for this peermap server (default: auto-detect)")
	Cmd.Flags().StringSlice("stun", []string{}, "stun server for peers NAT traversal (empty disable NAT traversal)")
}

func processOptions(cmd *cobra.Command) (opts peermap.Options, err error) {
	opts.Listen, err = cmd.Flags().GetString("listen")
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
