package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/rkonfj/peerguard/peermap/network"
	"github.com/rkonfj/peerguard/peermap/oidc"
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
			secret, err := cmd.Flags().GetString("secret")
			if err != nil {
				return err
			}
			if len(secret) == 0 {
				secret = login(peermapCluster)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return vpn.NewVPN(vpn.Config{
				MTU:     mtu,
				CIDR:    cidr,
				Peermap: peermapCluster,
				Network: secret,
			}).RunTun(ctx, tunName)
		},
	}
	Cmd.Flags().String("secret", "", "p2p network secret")
	Cmd.Flags().String("cidr", "", "is an IP address prefix (CIDR) representing an IP network.  i.e. 100.0.0.2/24")
	Cmd.Flags().StringSlice("peermap", []string{}, "peermap cluster")
	Cmd.Flags().String("tun", "pg0", "tun name")
	Cmd.Flags().Int("mtu", 1500-40-8, "mtu")

	Cmd.MarkFlagRequired("cidr")
	Cmd.MarkFlagRequired("peermap")
}

func login(peermapCluster []string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	netSecretFile := filepath.Join(homeDir, ".peerguard_network_secret")
	updateSecret := func() string {
		f, err := os.Create(netSecretFile)
		if err != nil {
			return ""
		}
		defer f.Close()
		joined, err := requestNetworkSecret(peermapCluster)
		if err != nil {
			slog.Error("RequestNetworkkSecret failed", "err", err)
			return ""
		}
		json.NewEncoder(f).Encode(joined)
		slog.Info("NetworkJoined", "network", joined.Network)
		return joined.SecretID
	}
	f, err := os.Open(netSecretFile)
	if os.IsNotExist(err) {
		return updateSecret()
	}
	if err != nil {
		return ""
	}
	defer f.Close()
	var joined oidc.NetworkSecret
	if err = json.NewDecoder(f).Decode(&joined); err != nil {
		return updateSecret()
	}
	if time.Until(joined.Expire) > 0 {
		return joined.SecretID
	}
	return updateSecret()
}

func requestNetworkSecret(peermapCluster []string) (*oidc.NetworkSecret, error) {
	join, err := network.JoinOIDC(peermapCluster[0], "google")
	if err != nil {
		slog.Error("JoinNetwork failed", "err", err)
		return nil, err
	}

	fmt.Println("Use the browser to open the following URL for authentication")
	qrterminal.GenerateWithConfig(join.AuthURL(), qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.WHITE,
		WhiteChar: qrterminal.BLACK,
		QuietZone: 1,
	})
	fmt.Println("AuthURL:", join.AuthURL())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return join.Wait(ctx)
}
