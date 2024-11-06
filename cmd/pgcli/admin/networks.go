package admin

import (
	"encoding/json"
	"flag"
	"os"

	"github.com/sigcn/pg/peermap/exporter"
)

func listNetworks(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("networks", flag.ExitOnError)
	secretKey, server, err := parseSecretKeyAndServer(flagSet, admin.Args()[1:])
	if err != nil {
		return err
	}

	c, err := exporter.NewClient(server, secretKey)
	if err != nil {
		return err
	}
	networks, err := c.Networks()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(networks)
}
