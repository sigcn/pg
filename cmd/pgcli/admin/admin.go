package admin

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:          "admin",
		Short:        "The pgmap manager tool",
		SilenceUsage: true,
	}
	Cmd.PersistentFlags().String("secret-key", "", "key to generate network secret")
	Cmd.AddCommand(secretCmd())
	Cmd.AddCommand(networksCmd())
	Cmd.AddCommand(peersCmd())
	Cmd.AddCommand(putMetaCmd())
	Cmd.AddCommand(getMetaCmd())
}

func requiredArg(flagSet *pflag.FlagSet, argName string) (string, error) {
	value := os.Getenv(fmt.Sprintf("PG_%s", strings.ReplaceAll(strings.ToUpper(argName), "-", "_")))
	arg, err := flagSet.GetString(argName)
	if err != nil {
		return "", err
	}
	if arg != "" {
		value = arg
	}
	if value == "" {
		return value, fmt.Errorf("required flag \"%s\" not set", argName)
	}
	return value, nil
}
