package vpn

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/sigcn/pg/cmd/pgcli/vpn/ipc/client"
	"github.com/sigcn/pg/cmd/pgcli/vpn/ipc/server"
	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/disco/udp"
	"github.com/sigcn/pg/p2p"
	"github.com/sigcn/pg/peermap/network"
	"github.com/sigcn/pg/secure/aescbc"
	"github.com/sigcn/pg/secure/chacha20poly1305"
	"github.com/sigcn/pg/vpn"
	"github.com/sigcn/pg/vpn/nic"
	"github.com/sigcn/pg/vpn/nic/tun"
)

var (
	Version string
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func Run(args []string) error {
	flagSet := flag.NewFlagSet("vpn", flag.ExitOnError)
	flagSet.Usage = func() { usage(flagSet) }
	flagSet.BoolFunc("v", flag.Lookup("v").Usage, flag.Lookup("v").Value.Set)
	flagSet.BoolFunc("version", "", flag.Lookup("v").Value.Set)

	var logLevel int
	flagSet.IntVar(&logLevel, "loglevel", 0, "log level")
	flagSet.IntVar(&logLevel, "V", 0, "")
	cfg, err := createConfig(flagSet, args)
	if err != nil {
		return err
	}

	if cfg.QueryPeers {
		return client.PrintPeers()
	}

	slog.SetLogLoggerLevel(slog.Level(logLevel))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return (&P2PVPN{Config: cfg}).Run(ctx)
}

func usage(flagSet *flag.FlagSet) {
	ipv4 := flagSet.Lookup("4")
	ipv6 := flagSet.Lookup("6")
	authQR := flagSet.Lookup("auth-qr")
	discoChallengesBackoffRate := flagSet.Lookup("disco-challenges-backoff-rate")
	discoChallengesInitialInterval := flagSet.Lookup("disco-challenges-initial-interval")
	discoChallengesRetry := flagSet.Lookup("disco-challenges-retry")
	discoIgnoredInterface := flagSet.Lookup("disco-ignored-interface")
	discoPortScanCount := flagSet.Lookup("disco-port-scan-count")
	discoPortScanDuration := flagSet.Lookup("disco-port-scan-duration")
	discoPortScanOffset := flagSet.Lookup("disco-port-scan-offset")
	cryptoAlgo := flagSet.Lookup("udp-crypto")
	secretFile := flagSet.Lookup("f")
	forcePeerRelay := flagSet.Lookup("force-peer-relay")
	forceServerRelay := flagSet.Lookup("force-server-relay")
	key := flagSet.Lookup("key")
	logLevel := flagSet.Lookup("loglevel")
	mtu := flagSet.Lookup("mtu")
	peers := flagSet.Lookup("peers")
	pprof := flagSet.Lookup("pprof")
	server := flagSet.Lookup("s")
	tun := flagSet.Lookup("tun")
	udpPort := flagSet.Lookup("udp-port")
	version := flagSet.Lookup("v")

	fmt.Printf("Run a vpn daemon which backend is PeerGuard p2p network\n\n")
	fmt.Printf("Usage: %s [flags]\n\n", flagSet.Name())
	fmt.Printf("Daemon Flags:\n")
	fmt.Printf("  -4, --ipv4 string\n\t%s\n", ipv4.Usage)
	fmt.Printf("  -6, --ipv6 string\n\t%s\n", ipv6.Usage)
	fmt.Printf("  --auth-qr\n\t%s\n", authQR.Usage)
	fmt.Printf("  --disco-challenges-backoff-rate float\n\t%s (default %s)\n", discoChallengesBackoffRate.Usage, discoChallengesBackoffRate.DefValue)
	fmt.Printf("  --disco-challenges-initial-interval duration\n\t%s (default %s)\n", discoChallengesInitialInterval.Usage, discoChallengesInitialInterval.DefValue)
	fmt.Printf("  --disco-challenges-retry int\n\t%s (default %s)\n", discoChallengesRetry.Usage, discoChallengesRetry.DefValue)
	fmt.Printf("  --disco-ignored-interface []string\n\t%s\n", discoIgnoredInterface.Usage)
	fmt.Printf("  --disco-port-scan-count int\n\t%s (default %s)\n", discoPortScanCount.Usage, discoPortScanCount.DefValue)
	fmt.Printf("  --disco-port-scan-duration duration\n\t%s (default %s)\n", discoPortScanDuration.Usage, discoPortScanDuration.DefValue)
	fmt.Printf("  --disco-port-scan-offset int\n\t%s (default %s)\n", discoPortScanOffset.Usage, discoPortScanOffset.DefValue)
	fmt.Printf("  -f, --secret-file string\n\t%s\n", secretFile.Usage)
	fmt.Printf("  --force-peer-relay \n\t%s\n", forcePeerRelay.Usage)
	fmt.Printf("  --force-server-relay \n\t%s\n", forceServerRelay.Usage)
	fmt.Printf("  --key string\n\t%s\n", key.Usage)
	fmt.Printf("  --loglevel int\n\t%s (default %s)\n", logLevel.Usage, logLevel.DefValue)
	fmt.Printf("  --mtu int\n\t%s (default %s)\n", mtu.Usage, mtu.DefValue)
	fmt.Printf("  --pprof \n\t%s\n", pprof.Usage)
	fmt.Printf("  -s, --server string\n\t%s\n", server.Usage)
	fmt.Printf("  --tun string\n\t%s (default %s)\n", tun.Usage, tun.DefValue)
	fmt.Printf("  --udp-crypto\n\t%s (default %s)\n", cryptoAlgo.Usage, cryptoAlgo.DefValue)
	fmt.Printf("  --udp-port int\n\t%s (default %s)\n\n", udpPort.Usage, udpPort.DefValue)
	fmt.Printf("IPC Flags:\n")
	fmt.Printf("  --peers \n\t%s\n\n", peers.Usage)
	fmt.Printf("Global Flags:\n")
	fmt.Printf("  -h, --help\n\tshow help\n")
	fmt.Printf("  -v, --version\n\t%s\n", version.Usage)
}

func createConfig(flagSet *flag.FlagSet, args []string) (cfg Config, err error) {
	// ipc flags
	var queryPeers bool

	// daemon flags
	var forcePeerRelay, forceServerRelay bool
	var ignoredInterfaces stringSlice
	var cryptoAlgo string

	flagSet.IntVar(&cfg.DiscoPortScanOffset, "disco-port-scan-offset", -1000, "scan ports offset when disco")
	flagSet.IntVar(&cfg.DiscoPortScanCount, "disco-port-scan-count", 3000, "scan ports count when disco")
	flagSet.DurationVar(&cfg.DiscoPortScanDuration, "disco-port-scan-duration", 6*time.Second, "scan ports duration when disco")
	flagSet.IntVar(&cfg.DiscoChallengesRetry, "disco-challenges-retry", 5, "ping challenges retry count when disco")
	flagSet.DurationVar(&cfg.DiscoChallengesInitialInterval, "disco-challenges-initial-interval", 200*time.Millisecond, "ping challenges initial interval when disco")
	flagSet.Float64Var(&cfg.DiscoChallengesBackoffRate, "disco-challenges-backoff-rate", 1.65, "ping challenges backoff rate when disco")
	flagSet.Var(&ignoredInterfaces, "disco-ignored-interface", "ignore interfaces prefix when disco")

	flagSet.StringVar(&cfg.NICConfig.IPv4, "ipv4", "", "")
	flagSet.StringVar(&cfg.NICConfig.IPv4, "4", "", "ipv4 address prefix (e.g. 100.99.0.1/24)")
	flagSet.StringVar(&cfg.NICConfig.IPv6, "ipv6", "", "")
	flagSet.StringVar(&cfg.NICConfig.IPv6, "6", "", "ipv6 address prefix (e.g. fd00::1/64)")
	flagSet.IntVar(&cfg.NICConfig.MTU, "mtu", 1411, "nic mtu")
	flagSet.StringVar(&cfg.NICConfig.Name, "tun", defaultTunName, "nic name")

	flagSet.StringVar(&cfg.PrivateKey, "key", "", "curve25519 private key in base58 format (default generate a new one)")
	flagSet.StringVar(&cfg.SecretFile, "secret-file", "", "")
	flagSet.StringVar(&cfg.SecretFile, "f", "", "p2p network secret file (default ~/.peerguard_network_secret.json)")
	flagSet.BoolVar(&cfg.AuthQR, "auth-qr", false, "display the QR code when authentication is required")
	flagSet.BoolVar(&cfg.PProf, "pprof", false, "enable http pprof server")
	flagSet.StringVar(&cfg.Server, "server", os.Getenv("PG_SERVER"), "")
	flagSet.StringVar(&cfg.Server, "s", os.Getenv("PG_SERVER"), "peermap server")
	flagSet.BoolVar(&queryPeers, "peers", false, "query found peers")

	flagSet.StringVar(&cryptoAlgo, "udp-crypto", "chacha20poly1305", "udp packet crypto algorithm from the list [chacha20poly1305, aescbc]")
	flagSet.IntVar(&cfg.Port, "udp-port", 29877, "p2p udp listen port")
	flagSet.BoolVar(&forcePeerRelay, "force-peer-relay", false, "force to peer relay transport mode")
	flagSet.BoolVar(&forceServerRelay, "force-server-relay", false, "force to server relay transport mode")

	flagSet.Parse(args)

	switch cryptoAlgo {
	case "chacha20poly1305":
		p2p.SetDefaultSymmAlgo(chacha20poly1305.New)
	case "aescbc":
		p2p.SetDefaultSymmAlgo(aescbc.New)
	default:
		slog.Warn("Fallback to default chacha20poly1305")
	}

	cfg.DiscoIgnoredInterfaces = ignoredInterfaces
	cfg.QueryPeers = queryPeers

	if cfg.QueryPeers {
		return
	}

	if cfg.Server == "" {
		err = errors.New("flag \"server\" not set")
		return
	}
	if cfg.NICConfig.IPv4 == "" && cfg.NICConfig.IPv6 == "" {
		err = errors.New("at least one of the flags in the group [ipv4 ipv6] is required")
		return
	}
	if forcePeerRelay {
		cfg.P2pTransportMode = p2p.MODE_FORCE_PEER_RELAY
	}
	if forceServerRelay && !forcePeerRelay {
		cfg.P2pTransportMode = p2p.MODE_FORCE_RELAY
	}
	return
}

type Config struct {
	NICConfig                      nic.Config
	DiscoPortScanOffset            int
	DiscoPortScanCount             int
	DiscoPortScanDuration          time.Duration
	DiscoChallengesRetry           int
	DiscoChallengesInitialInterval time.Duration
	DiscoChallengesBackoffRate     float64
	DiscoIgnoredInterfaces         []string
	QueryPeers                     bool
	Port                           int
	PrivateKey                     string
	SecretFile                     string
	Server                         string
	AuthQR                         bool
	PProf                          bool
	P2pTransportMode               p2p.TransportMode
}

type P2PVPN struct {
	Config Config
	nic    *nic.VirtualNIC
}

func (v *P2PVPN) Run(ctx context.Context) error {
	tunnic, err := tun.Create(v.Config.NICConfig)
	if err != nil {
		return err
	}
	c, err := v.listenPacketConn(ctx)
	if err != nil {
		err1 := tunnic.Close()
		return errors.Join(err, err1)
	}
	c.SetTransportMode(v.Config.P2pTransportMode)
	v.nic = &nic.VirtualNIC{NIC: tunnic}
	var wg sync.WaitGroup
	defer wg.Wait()
	if err := (&server.Server{
		EnablePProf: v.Config.PProf,
		Vnic:        v.nic,
		PeerStore:   c.PeerStore(),
		Meta:        c.PeerMeta}).Start(ctx, &wg); err != nil {
		slog.Warn("[IPC] RunServer", "err", err)
	}
	return vpn.New(vpn.Config{
		MTU:           v.Config.NICConfig.MTU,
		OnRouteAdd:    func(dst net.IPNet, _ net.IP) { disco.AddIgnoredLocalCIDRs(dst.String()) },
		OnRouteRemove: func(dst net.IPNet, _ net.IP) { disco.RemoveIgnoredLocalCIDRs(dst.String()) },
	}).Run(ctx, v.nic, c)
}

func (v *P2PVPN) listenPacketConn(ctx context.Context) (c *p2p.PacketConn, err error) {
	udp.SetModifyDiscoConfig(func(cfg *udp.DiscoConfig) {
		cfg.PortScanOffset = v.Config.DiscoPortScanOffset
		cfg.PortScanCount = v.Config.DiscoPortScanCount
		cfg.PortScanDuration = v.Config.DiscoPortScanDuration
		cfg.ChallengesRetry = v.Config.DiscoChallengesRetry
		cfg.ChallengesInitialInterval = v.Config.DiscoChallengesInitialInterval
		cfg.ChallengesBackoffRate = v.Config.DiscoChallengesBackoffRate
	})
	v.Config.DiscoIgnoredInterfaces = append(v.Config.DiscoIgnoredInterfaces, "pg", "wg", "veth", "docker", "nerdctl", "tailscale")
	disco.SetIgnoredLocalInterfaceNamePrefixs(v.Config.DiscoIgnoredInterfaces...)

	hostname, _ := os.Hostname()
	p2pOptions := []p2p.Option{
		p2p.PeerMeta("version", Version),
		p2p.PeerMeta("name", hostname),
		p2p.ListenPeerUp(v.addPeer),
		p2p.ListenPeerLeave(v.removePeer),
		p2p.KeepAlivePeriod(6 * time.Second),
	}
	if v.Config.Port > 0 {
		p2pOptions = append(p2pOptions, p2p.ListenUDPPort(v.Config.Port))
	}
	if v.Config.NICConfig.IPv4 != "" {
		ipv4, err := netip.ParsePrefix(v.Config.NICConfig.IPv4)
		if err != nil {
			return nil, err
		}
		disco.AddIgnoredLocalCIDRs(v.Config.NICConfig.IPv4)
		p2pOptions = append(p2pOptions, p2p.PeerAlias1(ipv4.Addr().String()))
	}
	if v.Config.NICConfig.IPv6 != "" {
		ipv6, err := netip.ParsePrefix(v.Config.NICConfig.IPv6)
		if err != nil {
			return nil, err
		}
		disco.AddIgnoredLocalCIDRs(v.Config.NICConfig.IPv6)
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
	peermap, err := disco.NewServer(v.Config.Server, secretStore)
	if err != nil {
		return
	}

	return p2p.ListenPacketContext(ctx, peermap, p2pOptions...)
}

func (v *P2PVPN) addPeer(pi disco.PeerID, m url.Values) {
	v.nic.AddPeer(nic.Peer{Addr: pi, IPv4: m.Get("alias1"), IPv6: m.Get("alias2"), Meta: m})
}

func (v *P2PVPN) removePeer(pi disco.PeerID) {
	v.nic.RemovePeer(pi)
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
