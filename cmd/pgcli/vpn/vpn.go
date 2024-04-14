package vpn

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/mdp/qrterminal/v3"
	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/p2p"
	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peer/peermap"
	"github.com/rkonfj/peerguard/peermap/network"
	"github.com/rkonfj/peerguard/peermap/oidc"
	"github.com/rkonfj/peerguard/vpn"
	"github.com/rkonfj/peerguard/vpn/link"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:   "vpn",
	Short: "Run a vpn daemon which backend is PeerGuard p2p network",
	Args:  cobra.NoArgs,
	RunE:  run,
}

func init() {
	Cmd.Flags().StringP("ipv4", "4", "", "ipv4 address prefix (i.e. 100.99.0.1/24)")
	Cmd.Flags().StringP("ipv6", "6", "", "ipv6 address prefix (i.e. fd00::1/64)")
	Cmd.Flags().String("tun", "pg0", "tun name")
	Cmd.Flags().Int("mtu", 1428, "mtu")

	Cmd.Flags().String("key", "", "curve25519 private key in base64-url format (default generate a new one)")
	Cmd.Flags().String("secret-file", "", "p2p network secret file (default ~/.peerguard_network_secret.json)")

	Cmd.Flags().StringP("server", "s", "", "peermap server")
	Cmd.Flags().StringSlice("allowed-ip", []string{}, "declare IPs that can be routed/NATed by this machine (i.e. 192.168.0.0/24)")
	Cmd.Flags().StringSlice("peer", []string{}, "specify peers instead of auto-discovery (pg://<peerID>?alias1=<ipv4>&alias2=<ipv6>)")

	Cmd.Flags().Int("disco-port-scan-count", 2000, "scan ports count when disco")
	Cmd.Flags().Int("disco-challenges-retry", 2, "ping challenges retry count when disco")
	Cmd.Flags().Duration("disco-challenges-initial-interval", 200*time.Millisecond, "ping challenges initial interval when disco")
	Cmd.Flags().Float64("disco-challenges-backoff-rate", 1.75, "ping challenges backoff rate when disco")

	Cmd.MarkFlagRequired("server")
	Cmd.MarkFlagsOneRequired("ipv4", "ipv6")
}

func run(cmd *cobra.Command, args []string) (err error) {
	discoPortScanCount, err := cmd.Flags().GetInt("disco-port-scan-count")
	if err != nil {
		return
	}
	discoChallengesRetry, err := cmd.Flags().GetInt("disco-challenges-retry")
	if err != nil {
		return
	}
	discoChallengesInitialInterval, err := cmd.Flags().GetDuration("disco-challenges-initial-interval")
	if err != nil {
		return
	}
	discoChallengesBackoffRate, err := cmd.Flags().GetFloat64("disco-challenges-backoff-rate")

	cfg := vpn.Config{
		OnRoute: onRoute,
		ModifyDiscoConfig: func(cfg *disco.DiscoConfig) {
			cfg.PortScanCount = discoPortScanCount
			cfg.ChallengesRetry = discoChallengesRetry
			cfg.ChallengesInitialInterval = discoChallengesInitialInterval
			cfg.ChallengesBackoffRate = discoChallengesBackoffRate
		},
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

	server, err := cmd.Flags().GetString("server")
	if err != nil {
		return
	}

	tunName, err := cmd.Flags().GetString("tun")
	if err != nil {
		return
	}

	secretFile, err := cmd.Flags().GetString("secret-file")
	if err != nil {
		return err
	}
	if len(secretFile) == 0 {
		currentUser, err := user.Current()
		if err != nil {
			return err
		}
		secretFile = filepath.Join(currentUser.HomeDir, ".peerguard_network_secret.json")
	}

	secretStore, err := loginIfNecessary(server, secretFile)
	if err != nil {
		return err
	}

	peermapURL, _ := url.Parse(server)
	cfg.Peermap, err = peermap.New(peermapURL, secretStore)
	if err != nil {
		return err
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

func loginIfNecessary(peermap, secretFile string) (peer.SecretStore, error) {
	store := p2p.FileSecretStore(secretFile)
	newFileStore := func() (peer.SecretStore, error) {
		joined, err := requestNetworkSecret(peermap)
		if err != nil {
			return nil, fmt.Errorf("request network secret failed: %w", err)
		}
		return store, store.UpdateNetworkSecret(joined)
	}

	if _, err := os.Stat(secretFile); os.IsNotExist(err) {
		return newFileStore()
	}
	secret, err := store.NetworkSecret()
	if err != nil {
		return nil, err
	}
	if secret.Expired() {
		return newFileStore()
	}
	return store, nil
}

func requestNetworkSecret(peermap string) (peer.NetworkSecret, error) {
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
		return peer.NetworkSecret{}, err
	}
	join, err := network.JoinOIDC(provider, peermap)
	if err != nil {
		slog.Error("JoinNetwork failed", "err", err)
		return peer.NetworkSecret{}, err
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
