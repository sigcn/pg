package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/sigcn/pg/cmd/pgcli/admin"
	"github.com/sigcn/pg/cmd/pgcli/curve25519"
	"github.com/sigcn/pg/cmd/pgcli/download"
	"github.com/sigcn/pg/cmd/pgcli/share"
	"github.com/sigcn/pg/cmd/pgcli/vpn"
)

var (
	Version = "dev"
)

func main() {
	var logLevel int
	flag.Usage = usage
	flag.IntVar(&logLevel, "loglevel", 0, "log level")
	flag.Parse()
	slog.SetLogLoggerLevel(slog.Level(logLevel))

	var err error
	switch flag.Arg(0) {
	case "admin":
		err = admin.Run()
	case "curve25519":
		err = curve25519.Run()
	case "download":
		err = download.Run()
	case "share":
		err = share.Run()
	case "vpn":
		err = vpn.Run()
	}
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf("A p2p network toolset\n\n")
	fmt.Printf("Usage:\n")
	fmt.Printf("  %s [command] [flags]\n\n", os.Args[0])
	fmt.Printf("Commands:\n")
	fmt.Printf("  admin\t\tThe pgmap manager tool\n")
	fmt.Printf("  curve25519\tGenerate a new curve25519 key pair\n")
	fmt.Printf("  download\tDownload shared file from peer\n")
	fmt.Printf("  share\t\tShare files to peers\n")
	fmt.Printf("  vpn\t\tRun a vpn daemon which backend is PeerGuard p2p network\n")
	fmt.Printf("\nGlobal Flags:\n")
	fmt.Printf("  --loglevel int\n\tlog level\n")
}
