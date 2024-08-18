package vpn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/rkonfj/peerguard/disco"
	"github.com/rkonfj/peerguard/disco/tp"
	"github.com/rkonfj/peerguard/p2p"
	"github.com/rkonfj/peerguard/peermap/network"
	"github.com/rkonfj/peerguard/vpn"
	"github.com/rkonfj/peerguard/vpn/iface"
	"github.com/spf13/cobra"
)

var (
	Cmd = &cobra.Command{
		Use:   "vpn",
		Short: "Run a vpn daemon which backend is PeerGuard p2p network",
		Args:  cobra.NoArgs,
		RunE:  run,
	}
	Version = "dev"
	Commit  string
)

func init() {
	Cmd.Flags().StringP("ipv4", "4", "", "ipv4 address prefix (e.g. 100.99.0.1/24)")
	Cmd.Flags().StringP("ipv6", "6", "", "ipv6 address prefix (e.g. fd00::1/64)")
	Cmd.Flags().String("tun", defaultTunName, "tun device name")
	Cmd.Flags().Int("mtu", 1428, "mtu")

	Cmd.Flags().String("key", "", "curve25519 private key in base58 format (default generate a new one)")
	Cmd.Flags().StringP("secret-file", "f", "", "p2p network secret file (default ~/.peerguard_network_secret.json)")
	Cmd.Flags().StringP("server", "s", os.Getenv("PG_SERVER"), "peermap server url")
	Cmd.Flags().StringSlice("peer", []string{}, "specify peers instead of auto-discovery (pg://<peerID>?alias1=<ipv4>&alias2=<ipv6>)")

	Cmd.Flags().Int("disco-port-scan-count", 2000, "scan ports count when disco")
	Cmd.Flags().Duration("disco-port-scan-duration", 6*time.Second, "scan ports duration when disco")
	Cmd.Flags().Int("disco-challenges-retry", 5, "ping challenges retry count when disco")
	Cmd.Flags().Duration("disco-challenges-initial-interval", 200*time.Millisecond, "ping challenges initial interval when disco")
	Cmd.Flags().Float64("disco-challenges-backoff-rate", 1.65, "ping challenges backoff rate when disco")
	Cmd.Flags().StringSlice("disco-ignored-interface", nil, "ignore interfaces prefix when disco")

	Cmd.Flags().Bool("pprof", false, "enable http pprof server")
	Cmd.Flags().Bool("auth-qr", false, "display the QR code when authentication is required")

	Cmd.MarkFlagsOneRequired("ipv4", "ipv6")
}

func run(cmd *cobra.Command, args []string) (err error) {
	pprof, err := cmd.Flags().GetBool("pprof")
	if err != nil {
		return
	}
	if pprof {
		l, err := net.Listen("tcp", ":29800")
		if err != nil {
			return fmt.Errorf("pprof: %w", err)
		}
		slog.Info("Serving pprof server", "addr", "http://0.0.0.0:29800")
		defer l.Close()
		go http.Serve(l, nil)
	}
	cfg, err := createConfig(cmd)
	if err != nil {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return (&P2PVPN{Config: cfg}).Run(ctx)
}

func createConfig(cmd *cobra.Command) (cfg Config, err error) {
	cfg.DiscoPortScanCount, err = cmd.Flags().GetInt("disco-port-scan-count")
	if err != nil {
		return
	}
	cfg.DiscoPortScanDuration, err = cmd.Flags().GetDuration("disco-port-scan-duration")
	if err != nil {
		return
	}
	cfg.DiscoChallengesRetry, err = cmd.Flags().GetInt("disco-challenges-retry")
	if err != nil {
		return
	}
	cfg.DiscoChallengesInitialInterval, err = cmd.Flags().GetDuration("disco-challenges-initial-interval")
	if err != nil {
		return
	}
	cfg.DiscoChallengesBackoffRate, err = cmd.Flags().GetFloat64("disco-challenges-backoff-rate")
	if err != nil {
		return
	}
	cfg.DiscoIgnoredInterfaces, err = cmd.Flags().GetStringSlice("disco-ignored-interface")
	if err != nil {
		return
	}
	cfg.IPv4, err = cmd.Flags().GetString("ipv4")
	if err != nil {
		return
	}
	cfg.IPv6, err = cmd.Flags().GetString("ipv6")
	if err != nil {
		return
	}
	cfg.MTU, err = cmd.Flags().GetInt("mtu")
	if err != nil {
		return
	}
	cfg.TunName, err = cmd.Flags().GetString("tun")
	if err != nil {
		return
	}
	cfg.Peers, err = cmd.Flags().GetStringSlice("peer")
	if err != nil {
		return
	}
	cfg.PrivateKey, err = cmd.Flags().GetString("key")
	if err != nil {
		return
	}
	cfg.SecretFile, err = cmd.Flags().GetString("secret-file")
	if err != nil {
		return
	}
	cfg.AuthQR, err = cmd.Flags().GetBool("auth-qr")
	if err != nil {
		return
	}
	cfg.Server, err = cmd.Flags().GetString("server")
	if err != nil {
		return
	}
	if cfg.Server == "" {
		err = errors.New("flag \"server\" not set")
		return
	}
	return
}

type Config struct {
	iface.Config
	DiscoPortScanCount             int
	DiscoPortScanDuration          time.Duration
	DiscoChallengesRetry           int
	DiscoChallengesInitialInterval time.Duration
	DiscoChallengesBackoffRate     float64
	DiscoIgnoredInterfaces         []string
	TunName                        string
	Peers                          []string
	PrivateKey                     string
	SecretFile                     string
	Server                         string
	AuthQR                         bool
}

type P2PVPN struct {
	Config Config
	iface  iface.Interface
}

func (v *P2PVPN) Run(ctx context.Context) error {
	iface, err := iface.Create(v.Config.TunName, v.Config.Config)
	if err != nil {
		return err
	}
	v.iface = iface
	c, err := v.listenPacketConn(ctx)
	if err != nil {
		err1 := iface.Close()
		return errors.Join(err, err1)
	}
	return vpn.New(vpn.Config{
		MTU:           v.Config.MTU,
		OnRouteAdd:    func(dst net.IPNet, _ net.IP) { disco.AddIgnoredLocalCIDRs(dst.String()) },
		OnRouteRemove: func(dst net.IPNet, _ net.IP) { disco.RemoveIgnoredLocalCIDRs(dst.String()) },
	}).Run(ctx, iface, c)
}

func (v *P2PVPN) listenPacketConn(ctx context.Context) (c net.PacketConn, err error) {
	tp.SetModifyDiscoConfig(func(cfg *tp.DiscoConfig) {
		cfg.PortScanCount = v.Config.DiscoPortScanCount
		cfg.PortScanDuration = v.Config.DiscoPortScanDuration
		cfg.ChallengesRetry = v.Config.DiscoChallengesRetry
		cfg.ChallengesInitialInterval = v.Config.DiscoChallengesInitialInterval
		cfg.ChallengesBackoffRate = v.Config.DiscoChallengesBackoffRate
	})
	v.Config.DiscoIgnoredInterfaces = append(v.Config.DiscoIgnoredInterfaces, "pg", "wg", "veth", "docker", "nerdctl", "tailscale")
	disco.SetIgnoredLocalInterfaceNamePrefixs(v.Config.DiscoIgnoredInterfaces...)

	p2pOptions := []p2p.Option{
		p2p.PeerMeta("version", fmt.Sprintf("%s-%s", Version, Commit)),
		p2p.ListenPeerUp(v.addPeer),
	}
	if len(v.Config.Peers) > 0 {
		p2pOptions = append(p2pOptions, p2p.PeerSilenceMode())
	}
	for _, peerURL := range v.Config.Peers {
		pgPeer, err := url.Parse(peerURL)
		if err != nil {
			continue
		}
		if pgPeer.Scheme != "pg" {
			return nil, fmt.Errorf("unsupport scheme %s", pgPeer.Scheme)
		}
		v.addPeer(disco.PeerID(pgPeer.Host), pgPeer.Query())
	}
	if v.Config.IPv4 != "" {
		ipv4, err := netip.ParsePrefix(v.Config.IPv4)
		if err != nil {
			return nil, err
		}
		disco.AddIgnoredLocalCIDRs(v.Config.IPv4)
		p2pOptions = append(p2pOptions, p2p.PeerAlias1(ipv4.Addr().String()))
	}
	if v.Config.IPv6 != "" {
		ipv6, err := netip.ParsePrefix(v.Config.IPv6)
		if err != nil {
			return nil, err
		}
		disco.AddIgnoredLocalCIDRs(v.Config.IPv6)
		p2pOptions = append(p2pOptions, p2p.PeerAlias2(ipv6.Addr().String()))
	}
	if v.Config.PrivateKey != "" {
		p2pOptions = append(p2pOptions, p2p.ListenPeerCurve25519(v.Config.PrivateKey))
	} else {
		p2pOptions = append(p2pOptions, p2p.ListenPeerSecure())
	}

	secretStore, err := v.loginIfNecessary(ctx)
	if err != nil {
		return
	}
	peermapURL, err := url.Parse(v.Config.Server)
	if err != nil {
		return
	}
	peermap, err := disco.NewPeermap(peermapURL, secretStore)
	if err != nil {
		return
	}

	return p2p.ListenPacketContext(ctx, peermap, p2pOptions...)
}

func (v *P2PVPN) addPeer(pi disco.PeerID, m url.Values) {
	v.iface.AddPeer(pi, m.Get("alias1"), m.Get("alias2"))
}

func (v *P2PVPN) loginIfNecessary(ctx context.Context) (disco.SecretStore, error) {
	if len(v.Config.SecretFile) == 0 {
		currentUser, err := user.Current()
		if err != nil {
			return nil, err
		}
		v.Config.SecretFile = filepath.Join(currentUser.HomeDir, ".peerguard_network_secret.json")
	}

	store := p2p.FileSecretStore(v.Config.SecretFile)
	newFileStore := func() (disco.SecretStore, error) {
		joined, err := v.requestNetworkSecret(ctx)
		if err != nil {
			return nil, fmt.Errorf("request network secret failed: %w", err)
		}
		return store, store.UpdateNetworkSecret(joined)
	}

	if _, err := os.Stat(v.Config.SecretFile); os.IsNotExist(err) {
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

func (v *P2PVPN) requestNetworkSecret(ctx context.Context) (disco.NetworkSecret, error) {
	join, err := network.JoinOIDC("", v.Config.Server)
	if err != nil {
		slog.Error("JoinNetwork failed", "err", err)
		return disco.NetworkSecret{}, err
	}
	fmt.Println("Open the following link to authenticate")
	fmt.Println(join.AuthURL())
	if v.Config.AuthQR {
		qrterminal.GenerateWithConfig(join.AuthURL(), qrterminal.Config{
			Level:     qrterminal.L,
			Writer:    os.Stdout,
			BlackChar: qrterminal.WHITE,
			WhiteChar: qrterminal.BLACK,
			QuietZone: 1,
		})
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return join.Wait(ctx)
}
