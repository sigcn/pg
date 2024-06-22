package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/rkonfj/peerguard/peermap/exporter"
	"github.com/spf13/cobra"
)

func getMetaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get-meta <network>",
		Short: "Query network metadata from pgmap",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secretKey, err := requiredArg(cmd.InheritedFlags(), "secret-key")
			if err != nil {
				return err
			}
			server, err := requiredArg(cmd.Flags(), "server")
			if err != nil {
				return err
			}
			c, err := exporter.NewClient(server, secretKey)
			if err != nil {
				return err
			}
			networkMeta, err := c.NetworkMeta(args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(networkMeta)
		},
	}
	cmd.Flags().StringP("server", "s", "", "peermap server url")
	return cmd
}

func putMetaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-meta <network>",
		Short: "Set network metadata to pgmap",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secretKey, err := requiredArg(cmd.InheritedFlags(), "secret-key")
			if err != nil {
				return err
			}
			server, err := requiredArg(cmd.Flags(), "server")
			if err != nil {
				return err
			}
			key, err := cmd.Flags().GetString("key")
			if err != nil {
				return err
			}
			value, err := cmd.Flags().GetString("value")
			if err != nil {
				return err
			}
			c, err := exporter.NewClient(server, secretKey)
			if err != nil {
				return err
			}
			networkMeta, err := c.NetworkMeta(args[0])
			if err != nil {
				return err
			}
			v := reflect.ValueOf(networkMeta).Elem().FieldByName(key)
			if !v.IsValid() {
				return fmt.Errorf("meta %s not found", key)
			}
			v.SetString(value)
			json.NewEncoder(os.Stdout).Encode(networkMeta)
			return c.PutNetworkMeta(args[0], *networkMeta)
		},
	}
	cmd.Flags().StringP("server", "s", "", "peermap server url")
	cmd.Flags().StringP("key", "k", "", "metadata key")
	cmd.Flags().StringP("value", "v", "", "metadata value")
	cmd.MarkFlagRequired("key")
	return cmd
}
