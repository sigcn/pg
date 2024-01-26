package vpn

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rkonfj/peerguard/vpn"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "vpn",
		Short: "Run vpn peer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ip, err := cmd.Flags().GetString("ip")
			if err != nil {
				return err
			}
			network, err := cmd.Flags().GetString("network")
			if err != nil {
				return err
			}
			mtu, err := cmd.Flags().GetInt("mtu")
			if err != nil {
				return err
			}
			peermapCluster, err := cmd.Flags().GetStringSlice("peermap")
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			vpn.NewVPN(vpn.Config{
				MTU:     mtu,
				TunName: "tun0",
				IPv4:    ip,
				Peermap: peermapCluster,
				Network: network,
			}).Run(ctx)
			return nil
		},
	}
	Cmd.Flags().String("network", "", "Network")
	Cmd.Flags().String("ip", "", "ipv4/ipv6 addr")
	Cmd.Flags().Int("mtu", 1200, "mtu")
	Cmd.Flags().StringSlice("peermap", []string{}, "Peermap cluster")

	Cmd.MarkFlagRequired("network")
	Cmd.MarkFlagRequired("ip")
	Cmd.MarkFlagRequired("peermap")
}
