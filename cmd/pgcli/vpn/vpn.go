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
	"github.com/sigcn/pg/cmd/pgcli/vpn/rootless"
	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/disco/udp"
	"github.com/sigcn/pg/p2p"
	"github.com/sigcn/pg/peermap/network"
	"github.com/sigcn/pg/secure/aescbc"
	"github.com/sigcn/pg/secure/chacha20poly1305"
	"github.com/sigcn/pg/vpn"
	"github.com/sigcn/pg/vpn/nic"
	"github.com/sigcn/pg/vpn/nic/gvisor"
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

	if cfg.QueryNodeInfo {
		return client.PrintNodeInfo()
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
	forward := flagSet.Lookup("forward")
	key := flagSet.Lookup("key")
	labels := flagSet.Lookup("l")
	logLevel := flagSet.Lookup("loglevel")
	mtu := flagSet.Lookup("mtu")
	peers := flagSet.Lookup("peers")
	nodeInfo := flagSet.Lookup("nodeinfo")
	proxyListen := flagSet.Lookup("proxy-listen")
	proxyUsers := flagSet.Lookup("proxy-user")
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
	fmt.Printf("  --disco-ignored-interface strings\n\t%s\n", discoIgnoredInterface.Usage)
	fmt.Printf("  --disco-port-scan-count int\n\t%s (default %s)\n", discoPortScanCount.Usage, discoPortScanCount.DefValue)
	fmt.Printf("  --disco-port-scan-duration duration\n\t%s (default %s)\n", discoPortScanDuration.Usage, discoPortScanDuration.DefValue)
	fmt.Printf("  --disco-port-scan-offset int\n\t%s (default %s)\n", discoPortScanOffset.Usage, discoPortScanOffset.DefValue)
	fmt.Printf("  -f, --secret-file string\n\t%s\n", secretFile.Usage)
	fmt.Printf("  --force-peer-relay \n\t%s\n", forcePeerRelay.Usage)
	fmt.Printf("  --force-server-relay \n\t%s\n", forceServerRelay.Usage)
	fmt.Printf("  --forward strings\n\t%s\n", forward.Usage)
	fmt.Printf("  --key string\n\t%s\n", key.Usage)
	fmt.Printf("  -l, --label strings\n\t%s\n", labels.Usage)
	fmt.Printf("  --loglevel int\n\t%s (default %s)\n", logLevel.Usage, logLevel.DefValue)
	fmt.Printf("  --mtu int\n\t%s (default %s)\n", mtu.Usage, mtu.DefValue)
	fmt.Printf("  --proxy-listen string\n\t%s\n", proxyListen.Usage)
	fmt.Printf("  --proxy-user strings\n\t%s\n", proxyUsers.Usage)
	fmt.Printf("  -s, --server string\n\t%s\n", server.Usage)
	fmt.Printf("  --tun string\n\t%s (default %s)\n", tun.Usage, tun.DefValue)
	fmt.Printf("  --udp-crypto string\n\t%s (default %s)\n", cryptoAlgo.Usage, cryptoAlgo.DefValue)
	fmt.Printf("  --udp-port int\n\t%s (default %s)\n\n", udpPort.Usage, udpPort.DefValue)
	fmt.Printf("IPC Flags:\n")
	fmt.Printf("  --nodeinfo \n\t%s\n", nodeInfo.Usage)
	fmt.Printf("  --peers \n\t%s\n\n", peers.Usage)
	fmt.Printf("Global Flags:\n")
	fmt.Printf("  -h, --help\n\tshow help\n")
	fmt.Printf("  -v, --version\n\t%s\n", version.Usage)
}

func createConfig(flagSet *flag.FlagSet, args []string) (cfg Config, err error) {
	// daemon flags
	var forcePeerRelay, forceServerRelay bool
	var ignoredInterfaces, forwards, proxyUsers, nodeLabels stringSlice
	var cryptoAlgo string

	flagSet.IntVar(&cfg.DiscoConfig.PortScanOffset, "disco-port-scan-offset", -1000, "scan ports offset when disco")
	flagSet.IntVar(&cfg.DiscoConfig.PortScanCount, "disco-port-scan-count", 3000, "scan ports count when disco")
	flagSet.DurationVar(&cfg.DiscoConfig.PortScanDuration, "disco-port-scan-duration", 6*time.Second, "scan ports duration when disco")
	flagSet.IntVar(&cfg.DiscoConfig.ChallengesRetry, "disco-challenges-retry", 5, "ping challenges retry count when disco")
	flagSet.DurationVar(&cfg.DiscoConfig.ChallengesInitialInterval, "disco-challenges-initial-interval", 200*time.Millisecond, "ping challenges initial interval when disco")
	flagSet.Float64Var(&cfg.DiscoConfig.ChallengesBackoffRate, "disco-challenges-backoff-rate", 1.65, "ping challenges backoff rate when disco")
	flagSet.Var(&ignoredInterfaces, "disco-ignored-interface", "ignore interfaces prefix when disco")

	flagSet.StringVar(&cfg.NICConfig.IPv4, "ipv4", "", "")
	flagSet.StringVar(&cfg.NICConfig.IPv4, "4", "", "ipv4 address prefix (e.g. 100.99.0.1/24)")
	flagSet.StringVar(&cfg.NICConfig.IPv6, "ipv6", "", "")
	flagSet.StringVar(&cfg.NICConfig.IPv6, "6", "", "ipv6 address prefix (e.g. fd00::1/64)")
	flagSet.IntVar(&cfg.NICConfig.MTU, "mtu", 1371, "nic mtu")
	flagSet.StringVar(&cfg.NICConfig.Name, "tun", defaultTunName, "nic name")
	flagSet.Var(&forwards, "forward", "start in rootless mode and create a port forward (e.g. tcp://127.0.0.1:80)")
	flagSet.StringVar(&cfg.ProxyConfig.Listen, "proxy-listen", "", "start a proxy server to access the PG network (e.g. 127.0.0.1:4090)")
	flagSet.Var(&proxyUsers, "proxy-user", "user:pass pair for proxy server authenticate (can be specified multiple times)")
	flagSet.StringVar(&cfg.PrivateKey, "key", "", "curve25519 private key in base58 format (default generate a new one)")
	flagSet.StringVar(&cfg.SecretFile, "secret-file", "", "")
	flagSet.StringVar(&cfg.SecretFile, "f", "", "p2p network secret file (default ~/.peerguard_network_secret.json)")
	flagSet.BoolVar(&cfg.AuthQR, "auth-qr", false, "display the QR code when authentication is required")
	flagSet.StringVar(&cfg.Server, "server", os.Getenv("PG_SERVER"), "")
	flagSet.StringVar(&cfg.Server, "s", os.Getenv("PG_SERVER"), "peermap server")
	flagSet.BoolVar(&cfg.QueryPeers, "peers", false, "query found peers")
	flagSet.BoolVar(&cfg.QueryNodeInfo, "nodeinfo", false, "get information about this node")

	flagSet.StringVar(&cryptoAlgo, "udp-crypto", "chacha20poly1305", "udp packet crypto algorithm from the list [chacha20poly1305, aescbc]")
	flagSet.IntVar(&cfg.UDPPort, "udp-port", 29877, "p2p udp listen port")
	flagSet.BoolVar(&forcePeerRelay, "force-peer-relay", false, "force to peer relay transport mode")
	flagSet.BoolVar(&forceServerRelay, "force-server-relay", false, "force to server relay transport mode")
	flagSet.Var(&nodeLabels, "label", "")
	flagSet.Var(&nodeLabels, "l", "key=value pair to describe peer node")

	flagSet.Parse(args)

	cfg.DiscoConfig.IgnoredInterfaces = ignoredInterfaces
	cfg.Forwards = forwards
	cfg.ProxyConfig.Users = proxyUsers
	cfg.Labels = nodeLabels

	if cfg.QueryPeers || cfg.QueryNodeInfo {
		return
	}

	switch cryptoAlgo {
	case "chacha20poly1305":
		p2p.SetDefaultSymmAlgo(chacha20poly1305.New)
	case "aescbc":
		p2p.SetDefaultSymmAlgo(aescbc.New)
	default:
		slog.Warn("Fallback to default chacha20poly1305")
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
	NICConfig        nic.Config           `yaml:"nic"`
	ProxyConfig      rootless.ProxyConfig `yaml:"proxy"`
	DiscoConfig      udp.DiscoConfig      `yaml:"disco"`
	UDPPort          int                  `yaml:"udp_port"`
	PrivateKey       string               `yaml:"private_key"`
	SecretFile       string               `yaml:"secret_file"`
	Server           string               `yaml:"server"`
	AuthQR           bool                 `yaml:"auth_qr"`
	P2pTransportMode p2p.TransportMode    `yaml:"transport_mode"`
	Forwards         []string             `yaml:"forwards"`
	Labels           []string             `yaml:"labels"`

	QueryPeers    bool
	QueryNodeInfo bool
}

type P2PVPN struct {
	Config Config
	nic    *nic.VirtualNIC
}

func (v *P2PVPN) Run(ctx context.Context) (err error) {
	rootlessMode := len(v.Config.Forwards) > 0 || v.Config.ProxyConfig.Listen != ""

	var card nic.NIC
	if rootlessMode {
		card = &gvisor.GvisorCard{Config: v.Config.NICConfig, Stack: rootless.CreateGvisorStack()}
	} else {
		card, err = tun.Create(v.Config.NICConfig)
		if err != nil {
			return err
		}
	}

	c, err := v.listenPacketConn(ctx)
	if err != nil {
		err1 := card.Close()
		return errors.Join(err, err1)
	}
	c.SetTransportMode(v.Config.P2pTransportMode)
	v.nic = &nic.VirtualNIC{NIC: card}

	var wg sync.WaitGroup
	defer wg.Wait()
	if rootlessMode {
		if err := (&rootless.ForwardEngine{
			GvisorCard: card.(*gvisor.GvisorCard),
			Forwards:   v.Config.Forwards}).Start(ctx, &wg); err != nil {
			return err
		}
		if v.Config.ProxyConfig.Listen != "" {
			if err := (&rootless.ProxyServer{
				GvisorCard: card.(*gvisor.GvisorCard),
				Config:     v.Config.ProxyConfig}).Start(ctx, &wg); err != nil {
				return err
			}
		}
	}

	if err := (&server.Server{
		Vnic:       v.nic,
		PacketConn: c,
		Version:    Version}).Start(ctx, &wg); err != nil {
		slog.Warn("[IPC] Run http server", "err", err)
	}
	return vpn.New(vpn.Config{
		MTU:           v.Config.NICConfig.MTU,
		OnRouteAdd:    func(dst net.IPNet, _ net.IP) { disco.AddIgnoredLocalCIDRs(dst.String()) },
		OnRouteRemove: func(dst net.IPNet, _ net.IP) { disco.RemoveIgnoredLocalCIDRs(dst.String()) },
	}).Run(ctx, v.nic, c)
}

func (v *P2PVPN) listenPacketConn(ctx context.Context) (c *p2p.PacketConn, err error) {
	udp.SetModifyDiscoConfig(func(cfg *udp.DiscoConfig) {
		*cfg = v.Config.DiscoConfig
	})
	v.Config.DiscoConfig.IgnoredInterfaces = append(v.Config.DiscoConfig.IgnoredInterfaces, "pg", "wg", "veth", "docker", "nerdctl", "tailscale")
	disco.SetIgnoredLocalInterfaceNamePrefixs(v.Config.DiscoConfig.IgnoredInterfaces...)

	hostname, _ := os.Hostname()
	p2pOptions := []p2p.Option{
		p2p.PeerMeta("version", Version),
		p2p.PeerMeta("name", hostname),
		p2p.PeerMeta("st", fmt.Sprintf("%d", time.Now().Unix())),
		p2p.ListenPeerUp(v.onPeerUp),
		p2p.ListenPeerLeave(v.onPeerLeave),
		p2p.KeepAlivePeriod(6 * time.Second),
	}
	for _, l := range v.Config.Labels {
		p2pOptions = append(p2pOptions, p2p.PeerMeta("label", l))
	}
	if v.Config.UDPPort > 0 {
		p2pOptions = append(p2pOptions, p2p.ListenUDPPort(v.Config.UDPPort))
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

func (v *P2PVPN) onPeerUp(pi disco.PeerID, m url.Values) {
	v.nic.AddPeer(nic.Peer{Addr: pi, IPv4: m.Get("alias1"), IPv6: m.Get("alias2"), Meta: m})
}

func (v *P2PVPN) onPeerLeave(pi disco.PeerID) {
	v.nic.LabelPeer(pi, "node.off")
}

func (v *P2PVPN) loginIfNecessary(ctx context.Context) (disco.SecretStore, error) {
	if len(v.Config.SecretFile) == 0 {
		currentUser, err := user.Current()
		if err != nil {
			return nil, err
		}
		v.Config.SecretFile = filepath.Join(currentUser.HomeDir, ".peerguard_network_secret.json")
	}

	store := &disco.SecretFile{FilePath: v.Config.SecretFile}
	newFileStore := func() (disco.SecretStore, error) {
		joined, err := v.requestNetworkSecret(ctx)
		if err != nil {
			return nil, fmt.Errorf("request network secret failed: %w", err)
		}
		fmt.Println("Authentication successful")
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
