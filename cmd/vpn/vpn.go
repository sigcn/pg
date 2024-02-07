package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/mdp/qrterminal/v3"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/network"
	"github.com/rkonfj/peerguard/peermap/oidc"
	"github.com/rkonfj/peerguard/vpn"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "vpn",
		Short: "Run a vpn daemon powered by PeerGuard",
		Args:  cobra.NoArgs,
		RunE:  run,
	}
	Cmd.Flags().String("ipv4", "", "ipv4 address prefix.  i.e. 100.99.0.1/24")
	Cmd.Flags().String("ipv6", "", "ipv6 address prefix.  i.e. fd00::1/64")
	Cmd.Flags().String("tun", "pg0", "tun name")
	Cmd.Flags().Int("mtu", 1391, "mtu")
	Cmd.Flags().String("secret", "", "p2p network secret (default obtained using OIDC)")
	Cmd.Flags().StringSlice("peermap", []string{}, "peermap cluster")
	Cmd.Flags().StringSlice("allowed-ips", []string{}, "declare IPs that can be routed by this machine")
	Cmd.Flags().String("key", "", "curve25519 private key in base64-url format (default generate a new one)")

	Cmd.MarkFlagRequired("peermap")
}

func run(cmd *cobra.Command, args []string) (err error) {
	tunName, err := cmd.Flags().GetString("tun")
	if err != nil {
		return
	}

	cfg := vpn.Config{}
	cfg.IPv4, err = cmd.Flags().GetString("ipv4")
	if err != nil {
		return
	}
	cfg.IPv6, err = cmd.Flags().GetString("ipv6")
	if err != nil {
		return
	}

	if cfg.IPv4 == "" && cfg.IPv6 == "" {
		return errors.New("must specify at least one of ipv4 or ipv6")
	}

	cfg.AllowedIPs, err = cmd.Flags().GetStringSlice("allowed-ips")
	if err != nil {
		return
	}

	cfg.MTU, err = cmd.Flags().GetInt("mtu")
	if err != nil {
		return
	}

	cfg.PrivateKey, err = cmd.Flags().GetString("key")
	if err != nil {
		return
	}

	cfg.Peermap, err = cmd.Flags().GetStringSlice("peermap")
	if err != nil {
		return
	}

	secret, err := cmd.Flags().GetString("secret")
	if err != nil {
		return err
	}
	cfg.NetworkSecret = peer.NetworkSecret(secret)
	if len(secret) == 0 {
		cfg.NetworkSecret = login(cfg.Peermap)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return vpn.New(cfg).RunTun(ctx, tunName)
}

func login(peermapCluster []string) peer.NetworkSecret {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	netSecretFile := filepath.Join(homeDir, ".peerguard_network_secret.json")
	updateSecret := func() peer.NetworkSecret {
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
		return joined.Secret
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
		return joined.Secret
	}
	return updateSecret()
}

func requestNetworkSecret(peermapCluster []string) (*oidc.NetworkSecret, error) {
	prompt := promptui.Select{
		Label:    "Select OpenID Connect Provider",
		Items:    []string{oidc.ProviderGoogle, oidc.ProviderGithub},
		HideHelp: true,
		Templates: &promptui.SelectTemplates{
			Label:  "🔑 {{.}}",
			Active: "> {{.}}",
		},
	}
	_, provider, err := prompt.Run()
	if err != nil {
		return nil, err
	}
	join, err := network.JoinOIDC(provider, peermapCluster)
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
