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

	"github.com/manifoldco/promptui"
	"github.com/mdp/qrterminal/v3"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peermap/network"
	"github.com/rkonfj/peerguard/peermap/oidc"
	"github.com/rkonfj/peerguard/vpn"
	"github.com/rkonfj/peerguard/vpn/link"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "vpn",
	Short: "Run a vpn daemon powered by PeerGuard",
	Args:  cobra.NoArgs,
	RunE:  run,
}

func init() {
	Cmd.Flags().String("ipv4", "", "ipv4 address prefix (i.e. 100.99.0.1/24)")
	Cmd.Flags().String("ipv6", "", "ipv6 address prefix (i.e. fd00::1/64)")
	Cmd.Flags().String("tun", "pg0", "tun name")
	Cmd.Flags().Int("mtu", 1391, "mtu")

	Cmd.Flags().String("key", "", "curve25519 private key in base64-url format (default generate a new one)")
	Cmd.Flags().String("secret", "", "p2p network secret (default use OIDC to log into the network)")

	Cmd.Flags().StringSlice("peermap", []string{}, "peermap cluster")
	Cmd.Flags().StringSlice("allowed-ip", []string{}, "declare IPs that can be routed/NATed by this machine (i.e. 192.168.0.0/24)")
	Cmd.Flags().StringSlice("peer", []string{}, "specify peers instead of auto-discovery (pg://<peerID>?alias1=<ipv4>&alias2=<ipv6>)")

	Cmd.MarkFlagRequired("peermap")
	Cmd.MarkFlagsOneRequired("ipv4", "ipv6")
}

func run(cmd *cobra.Command, args []string) (err error) {
	tunName, err := cmd.Flags().GetString("tun")
	if err != nil {
		return
	}

	cfg := vpn.Config{
		OnRoute: onRoute,
	}
	cfg.IPv4, err = cmd.Flags().GetString("ipv4")
	if err != nil {
		return
	}
	cfg.IPv6, err = cmd.Flags().GetString("ipv6")
	if err != nil {
		return
	}

	cfg.AllowedIPs, err = cmd.Flags().GetStringSlice("allowed-ip")
	if err != nil {
		return
	}

	cfg.Peers, err = cmd.Flags().GetStringSlice("peer")
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
		cfg.NetworkSecret, err = login(cfg.Peermap)
		if err != nil {
			return err
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return vpn.New(cfg).RunTun(ctx, tunName)
}

func onRoute(route vpn.Route) {
	if len(route.OldDst) > 0 {
		for _, cidr := range route.OldDst {
			err := link.DelRoute(route.Device, cidr, route.Via)
			if err != nil {
				slog.Error("DelRoute error", "detail", err, "to", cidr, "via", route.Via)
			} else {
				slog.Info("DelRoute", "to", cidr, "via", route.Via)
			}
		}
	}
	if len(route.NewDst) > 0 {
		for _, cidr := range route.NewDst {
			err := link.AddRoute(route.Device, cidr, route.Via)
			if err != nil {
				slog.Error("AddRoute error", "detail", err, "to", cidr, "via", route.Via)
			} else {
				slog.Info("AddRoute", "to", cidr, "via", route.Via)
			}
		}
	}
}

func login(peermapCluster []string) (peer.NetworkSecret, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	netSecretFile := filepath.Join(homeDir, ".peerguard_network_secret.json")
	updateSecret := func() (peer.NetworkSecret, error) {
		f, err := os.Create(netSecretFile)
		if err != nil {
			return "", err
		}
		defer f.Close()
		joined, err := requestNetworkSecret(peermapCluster)
		if err != nil {
			return "", fmt.Errorf("request network secret failed: %w", err)
		}
		json.NewEncoder(f).Encode(joined)
		slog.Info("NetworkJoined", "network", joined.Network, "expire", joined.Expire)
		return joined.Secret, nil
	}
	f, err := os.Open(netSecretFile)
	if os.IsNotExist(err) {
		return updateSecret()
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	var joined oidc.NetworkSecret
	if err = json.NewDecoder(f).Decode(&joined); err != nil {
		return updateSecret()
	}
	if time.Until(joined.Expire) > 0 {
		return joined.Secret, nil
	}
	return updateSecret()
}

func requestNetworkSecret(peermapCluster []string) (*oidc.NetworkSecret, error) {
	prompt := promptui.Select{
		Label:    "Select OpenID Connect Provider",
		Items:    []string{oidc.ProviderGoogle, oidc.ProviderGithub},
		HideHelp: true,
		Templates: &promptui.SelectTemplates{
			Label:    "🔑 {{.}}",
			Active:   "> {{.}}",
			Selected: "{{green `✔`}} {{green .}} {{cyan `use the browser to open the following URL for authentication`}}",
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
	fmt.Println("AuthURL:", join.AuthURL())
	qrterminal.GenerateWithConfig(join.AuthURL(), qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.WHITE,
		WhiteChar: qrterminal.BLACK,
		QuietZone: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return join.Wait(ctx)
}
