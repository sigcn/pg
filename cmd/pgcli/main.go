package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/sigcn/pg/cmd/pgcli/admin"
	"github.com/sigcn/pg/cmd/pgcli/curve25519"
	"github.com/sigcn/pg/cmd/pgcli/download"
	"github.com/sigcn/pg/cmd/pgcli/share"
	"github.com/sigcn/pg/cmd/pgcli/vpn"
)

var (
	Version = "dev"

	funcMap map[string]func([]string) error = map[string]func([]string) error{
		"pgvpn": vpn.Run,
	}
)

func main() {
	commit, _, _ := readBinaryInfo()
	if len(commit) < 7 {
		vpn.Version = Version
	} else {
		vpn.Version = fmt.Sprintf("%s-%s", Version, commit[:7])
	}

	f, ok := funcMap[filepath.Base(os.Args[0])]
	if !ok {
		var logLevel int
		flag.Usage = usage
		flag.IntVar(&logLevel, "loglevel", 0, "log level")
		flag.BoolFunc("v", "print binary version", printVersion)
		flag.Parse()
		slog.SetLogLoggerLevel(slog.Level(logLevel))
		f = run
	}
	if err := f(os.Args[1:]); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch flag.Arg(0) {
	case "admin":
		return admin.Run()
	case "curve25519":
		return curve25519.Run()
	case "download":
		return download.Run()
	case "share":
		return share.Run()
	case "vpn":
		return vpn.Run(args[1:])
	default:
		usage()
		return nil
	}
}

func usage() {
	fmt.Printf("A p2p network toolset\n\n")
	fmt.Printf("Usage: %s [command] [flags]\n\n", os.Args[0])
	fmt.Printf("Commands:\n")
	fmt.Printf("  admin\t\tThe pgmap manager tool\n")
	fmt.Printf("  curve25519\tGenerate a new curve25519 key pair\n")
	fmt.Printf("  download\tDownload shared file from peer\n")
	fmt.Printf("  share\t\tShare files to peers\n")
	fmt.Printf("  vpn\t\tRun a vpn daemon which backend is PeerGuard p2p network\n\n")
	fmt.Printf("Flags:\n")
	fmt.Printf("  -v, --version\n\tprint binary version\n")
}

func readBinaryInfo() (commit, buildTime, goVersion string) {
	info, ok := debug.ReadBuildInfo()
	goVersion = info.GoVersion
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
	return
}

func printVersion(string) error {
	commit, buildTime, goVersion := readBinaryInfo()
	fmt.Println(goVersion)
	fmt.Printf("version\t\t%s\n", Version)
	fmt.Printf("build_time\t%s\n", buildTime)
	fmt.Printf("commit\t\t%s\n", commit)
	os.Exit(0)
	return nil
}
