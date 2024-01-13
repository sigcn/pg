package chat

import (
	"fmt"
	"strings"

	"github.com/rkonfj/peerguard/peer"
	"github.com/rkonfj/peerguard/peernet"
	"github.com/spf13/cobra"
)

var Cmd *cobra.Command

func init() {
	Cmd = &cobra.Command{
		Use:   "chat",
		Short: "PeerGuard chat example",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			network, err := cmd.Flags().GetString("network")
			if err != nil {
				return err
			}
			id, err := cmd.Flags().GetString("id")
			if err != nil {
				return err
			}
			servers, err := cmd.Flags().GetStringSlice("server")
			if err != nil {
				return err
			}
			return runInteraction(network, id, servers)
		},
	}
	Cmd.Flags().StringP("network", "n", "default", "network")
	Cmd.Flags().String("id", "", "peer id")
	Cmd.Flags().StringSliceP("server", "s", []string{}, "peermap server")

}

func runInteraction(network, peerID string, servers []string) error {
	node, err := peer.New(peernet.NetworkID(network), servers)
	if err != nil {
		return err
	}
	packetConn, err := node.ListenPacket(peernet.PeerID(peerID))
	if err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := packetConn.ReadFrom(buf)
			if err != nil {
				panic(err)
			}
			fmt.Printf("%s: %s\n", addr, string(buf[:n]))
		}
	}()

	for {
		var read string
		_, err := fmt.Scanln(&read)
		if err != nil {
			panic(err)
		}
		msg := strings.Split(read, ",")
		if len(msg) != 2 {
			fmt.Println("usage: peer,msg")
			continue
		}
		packetConn.WriteTo([]byte(msg[1]), peernet.PeerID(msg[0]))
	}
}
