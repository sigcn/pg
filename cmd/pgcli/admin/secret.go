package admin

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sigcn/pg/disco"
	"github.com/sigcn/pg/peermap/auth"
)

func generateSecret(admin *flag.FlagSet) error {
	flagSet := flag.NewFlagSet("secret", flag.ExitOnError)
	var secretKey, network, duration string
	flagSet.StringVar(&secretKey, "secret-key", "", "key to generate network secret")
	flagSet.StringVar(&network, "network", "default", "peermap server url")
	flagSet.StringVar(&duration, "duration", "24h", "secret duration to expire")

	flagSet.Parse(admin.Args()[1:])

	validDuration, err := time.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}

	if secretKey == "" {
		return errors.New("flag \"secret-key\" is required")
	}

	secret, err := auth.NewAuthenticator(secretKey).GenerateSecret(auth.Net{ID: network}, validDuration)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(disco.NetworkSecret{
		Secret:  secret,
		Network: network,
		Expire:  time.Now().Add(validDuration - 10*time.Second),
	})
}
