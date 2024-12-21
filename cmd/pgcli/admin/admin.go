package admin

import (
	"cmp"
	"flag"
	"fmt"
	"os"
)

func Run() error {
	admin := flag.NewFlagSet("admin", flag.ExitOnError)
	admin.Usage = func() { usageAdmin(admin) }
	admin.Parse(flag.Args()[1:])

	switch admin.Arg(0) {
	case "get-meta":
		return getMeta(admin)
	case "set-meta":
		return setMeta(admin)
	case "networks":
		return listNetworks(admin)
	case "peers":
		return listPeers(admin)
	case "secret":
		return generateSecret(admin)
	}
	usageAdmin(admin)
	return nil
}

func parseSecretKeyAndServer(flagSet *flag.FlagSet, args []string) (secretKey string, server string, err error) {
	flagSet.StringVar(&secretKey, "secret-key", "", "key to generate network secret")
	flagSet.StringVar(&secretKey, "s", "", "peermap server url")
	flagSet.Parse(args)

	secretKey = cmp.Or(secretKey, os.Getenv("PG_SECRET_KEY"))
	if secretKey == "" {
		err = fmt.Errorf("flag \"secret-key\" is required")
		return
	}

	server = cmp.Or(server, os.Getenv("PG_SERVER"))
	if server == "" {
		err = fmt.Errorf("flag \"server\" is required")
	}
	return
}

func usageAdmin(flagSet *flag.FlagSet) {
	fmt.Printf("Admin toolset\n\n")
	fmt.Printf("Usage: %s [subcommand]\n\n", flagSet.Name())
	fmt.Printf("Sub Commands:\n")
	fmt.Printf("  get-meta\tget network metadata\n")
	fmt.Printf("  set-meta\tset network metadata\n")
	fmt.Printf("  networks\tshow all networks\n")
	fmt.Printf("  peers\t\tshow all peers\n")
	fmt.Printf("  secret\tgenerate a network secret file\n")
}
