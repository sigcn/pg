package admin

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/peermap/auth"
)

func generateSecret(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("secret", flag.ExitOnError)
	var secretKey, network, alias, duration string
	flagSet.StringVar(&secretKey, "secret-key", "", "key to generate network secret")
	flagSet.StringVar(&network, "network", "", "peermap server url")
	flagSet.StringVar(&alias, "alias", "", "network alias")
	flagSet.StringVar(&duration, "duration", "", "secret duration to expire")

	flagSet.Parse(admin.Args()[1:])

	validDuration, err := time.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}

	secret, err := auth.NewAuthenticator(secretKey).GenerateSecret(auth.Net{
		Alias: alias,
		ID:    network,
	}, validDuration)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(disco.NetworkSecret{
		Secret:  secret,
		Network: network,
		Expire:  time.Now().Add(validDuration - 10*time.Second),
	})
}
