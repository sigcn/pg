package client

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/sigcn/pg/cmd/pgcli/vpn/ipc/server"
)

func PrintPeers() error {
	peers, err := (&server.ApiClient{}).QueryPeers()
	if err != nil {
		return err
	}
	tw := table.NewWriter()
	tw.AppendHeader(table.Row{
		"Node",
		"IPv4",
		"IPv6",
		"Mode",
		"NAT",
		"Flags",
		"UDP Endpoints",
		"Version",
	})

	for _, peer := range peers {
		var flags []string
		if _, ok := peer.Labels.Get("node.nr"); ok {
			flags = append(flags, "NR")
		}
		if _, ok := peer.Labels.Get("node.off"); ok {
			flags = append(flags, "OFF")
			peer.Mode = ""
		}
		tw.AppendRow(table.Row{
			cmp.Or(peer.Hostname, "-"),
			cmp.Or(peer.IPv4, "-"),
			cmp.Or(peer.IPv6, "-"),
			cmp.Or(peer.Mode, "-"),
			cmp.Or(peer.NAT, "-"),
			cmp.Or(strings.Join(flags, ","), "-"),
			cmp.Or(strings.Join(peer.Addrs, ","), "-"),
			peer.Version,
		})
	}

	tw.SetStyle(table.Style{Box: table.StyleBoxLight})
	fmt.Println(tw.Render())
	return nil
}
