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
		Short: "Run a vpn peer daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cidr, err := cmd.Flags().GetString("cidr")
			if err != nil {
				return err
			}
			network, err := cmd.Flags().GetString("network")
			if err != nil {
				return err
			}
			tunName, err := cmd.Flags().GetString("tun")
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
			return vpn.NewVPN(vpn.Config{
				MTU:     mtu,
				CIDR:    cidr,
				Peermap: peermapCluster,
				Network: network,
			}).RunTun(ctx, tunName)
		},
	}
	Cmd.Flags().String("network", "", "p2p network")
	Cmd.Flags().String("cidr", "", "is an IP address prefix (CIDR) representing an IP network.  i.e. 100.0.0.2/24")
	Cmd.Flags().StringSlice("peermap", []string{}, "peermap cluster")
	Cmd.Flags().String("tun", "pg0", "tun name")
	Cmd.Flags().Int("mtu", 1200, "mtu")

	Cmd.MarkFlagRequired("network")
	Cmd.MarkFlagRequired("cidr")
	Cmd.MarkFlagRequired("peermap")
}
