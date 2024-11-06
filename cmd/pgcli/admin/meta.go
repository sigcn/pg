package admin

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"

	"github.com/sigcn/pg/peermap/exporter"
)

func getMeta(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("get-meta", flag.ExitOnError)
	secretKey, server, err := parseSecretKeyAndServer(flagSet, admin.Args()[1:])
	if err != nil {
		return err
	}

	c, err := exporter.NewClient(server, secretKey)
	if err != nil {
		return err
	}
	networkMeta, err := c.NetworkMeta(flagSet.Arg(0))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(networkMeta)
}

func setMeta(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("get-meta", flag.ExitOnError)
	var key, value string
	flagSet.StringVar(&key, "key", "", "")
	flagSet.StringVar(&value, "value", "", "")
	secretKey, server, err := parseSecretKeyAndServer(flagSet, admin.Args()[1:])
	if err != nil {
		return err
	}

	c, err := exporter.NewClient(server, secretKey)
	if err != nil {
		return err
	}
	networkMeta, err := c.NetworkMeta(flagSet.Arg(0))
	if err != nil {
		return err
	}
	v := reflect.ValueOf(networkMeta).Elem().FieldByName(key)
	if !v.IsValid() {
		return fmt.Errorf("meta %s not found", key)
	}
	v.SetString(value)
	json.NewEncoder(os.Stdout).Encode(networkMeta)
	return c.PutNetworkMeta(flagSet.Arg(0), *networkMeta)
}
