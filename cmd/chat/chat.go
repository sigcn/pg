package chat

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"

	_ "net/http/pprof"

	"github.com/rkonfj/peerguard/p2p"
	"github.com/rkonfj/peerguard/peer"
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

	go func() {
		port := 3000 + rand.Intn(100)
		fmt.Printf("pprof: :%d/debug/pprof\n", port)
		http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
	}()

	node, err := p2p.New(
		peer.NetworkID(network),
		servers,
		p2p.ListenPeerID(peer.PeerID(peerID)),
	)
	if err != nil {
		return err
	}
	packetConn := node.ListenPacket()
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
		packetConn.WriteTo([]byte(msg[1]), peer.PeerID(msg[0]))
	}
}
