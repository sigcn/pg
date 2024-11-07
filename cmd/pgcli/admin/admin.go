package admin

import (
	"flag"
	"fmt"
	"os"
)

func Run() error {
	admin := flag.NewFlagSet("admin", flag.ExitOnError)
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
	return fmt.Errorf("unknown command \"%s\" for \"%s\"", admin.Arg(0), flag.CommandLine.Name())
}

func parseSecretKeyAndServer(flagSet *flag.FlagSet, args []string) (secretKey string, server string, err error) {
	flagSet.StringVar(&secretKey, "secret-key", "", "key to generate network secret")
	flagSet.StringVar(&secretKey, "server", "", "peermap server url")
	flagSet.Parse(args)

	if secretKey == "" {
		secretKey = os.Getenv("PG_SECRET_KEY")
	}

	if secretKey == "" {
		err = fmt.Errorf("flag \"secret-key\" is required")
		return
	}

	if server == "" {
		server = os.Getenv("PG_SERVER")
	}

	if server == "" {
		err = fmt.Errorf("flag \"server\" is required")
		server = os.Getenv("PG_SERVER")
	}
	return
}
