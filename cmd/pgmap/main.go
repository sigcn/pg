package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/sigcn/pg/peermap"
	"github.com/sigcn/pg/peermap/admin"
)

var (
	Version = "dev"
)

type stuns []string

func (s *stuns) String() string {
	return strings.Join(*s, ",")
}

func (s *stuns) Set(stun string) error {
	*s = append(*s, stun)
	return nil
}

func main() {
	var (
		configPath string
		logLevel   int
		stuns      stuns
	)
	flag.StringVar(&configPath, "config", "config.yml", "")
	flag.StringVar(&configPath, "c", "config.yml", "config file")
	flag.IntVar(&logLevel, "loglevel", int(slog.LevelInfo), "log level (default is 0:info)")
	flag.Var(&stuns, "stun", "stun server for peers NAT traversal (leave blank to disable NAT traversal)")

	commandConfig := peermap.Config{}
	flag.StringVar(&commandConfig.Listen, "listen", "127.0.0.1:9987", "")
	flag.StringVar(&commandConfig.Listen, "l", "127.0.0.1:9987", "listen http address")
	flag.StringVar(&commandConfig.SecretKey, "secret-key", "", "key to generate network secret (defaut generate a random one)")
	flag.StringVar(&commandConfig.PublicNetwork, "pubnet", "", "public network (leave blank to disable public network)")
	flag.BoolFunc("version", "", printVersion)
	flag.BoolFunc("v", "print version", printVersion)

	flag.Usage = usage
	flag.Parse()

	commandConfig.STUNs = stuns

	slog.SetLogLoggerLevel(slog.Level(logLevel))
	if err := run(commandConfig, configPath); err != nil {
		fmt.Printf("Error: %s\n", err)
		os.Exit(1)
	}
}

func printVersion(string) error {
	var buildTime, commit string
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, kv := range info.Settings {
			if kv.Key == "vcs.revision" {
				commit = kv.Value
				continue
			}
			if kv.Key == "vcs.time" {
				buildTime = kv.Value
			}
		}
	}
	fmt.Println(info.GoVersion)
	fmt.Printf("version %s\n", Version)
	fmt.Printf("build_time %s\n", buildTime)
	fmt.Printf("commit %s\n", commit)
	os.Exit(0)
	return nil
}

func usage() {
	fmt.Printf("Run a peermap server daemon\n\n")
	fmt.Printf("Usage of %s:\n", os.Args[0])
	fmt.Printf("  -c, --config string\n\t%s (default is \"%s\")\n", flag.Lookup("c").Usage, flag.Lookup("c").DefValue)
	fmt.Printf("  -l, --listen string\n\t%s (default is \"%s\")\n", flag.Lookup("l").Usage, flag.Lookup("l").DefValue)
	fmt.Printf("  --loglevel int\n\t%s\n", flag.Lookup("loglevel").Usage)
	fmt.Printf("  --pubnet string\n\t%s\n", flag.Lookup("pubnet").Usage)
	fmt.Printf("  --secret-key string\n\t%s\n", flag.Lookup("secret-key").Usage)
	fmt.Printf("  --stun []string\n\t%s\n", flag.Lookup("stun").Usage)
	fmt.Printf("  -v, --version\n\t%s\n", flag.Lookup("v").Usage)
}

func run(commandConfig peermap.Config, configPath string) error {
	cfg, _ := peermap.ReadConfig(configPath)
	cfg.Overwrite(commandConfig)

	admin.Version = Version
	srv, err := peermap.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
