package admin

import (
	"encoding/json"
	"flag"
	"os"

	"github.com/sigcn/pg/peermap/exporter"
)

func listPeers(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("peers", flag.ExitOnError)
	secretKey, server, err := parseSecretKeyAndServer(flagSet, admin.Args()[1:])
	if err != nil {
		return err
	}

	c, err := exporter.NewClient(server, secretKey)
	if err != nil {
		return err
	}
	peers, err := c.Peers()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(peers)
}
