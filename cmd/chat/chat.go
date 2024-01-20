package chat

import (
	"fmt"
	"math/rand"
	"net/http"

	_ "net/http/pprof"

	"github.com/rkonfj/peerguard/p2p"
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
	Cmd.Flags().StringP("network", "n", "", "network")
	Cmd.Flags().String("id", "", "peer id")
	Cmd.Flags().StringSliceP("server", "s", []string{}, "peermap server")

}

func runInteraction(network, peerID string, servers []string) error {

	go func() {
		port := 3000 + rand.Intn(100)
		fmt.Printf("pprof: :%d/debug/pprof\n", port)
		http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
	}()

	packetConn, err := p2p.ListenPacket(
		network,
		p2p.Peermap(servers...),
		p2p.ListenPeerID(peerID),
	)
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
			fmt.Println("ERR", err)
			continue
		}
		count, err := packetConn.Broadcast([]byte(read))
		if err != nil {
			panic(err)
		}
		fmt.Println(count, "peers sent")
	}
}
