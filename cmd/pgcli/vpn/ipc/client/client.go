package client

import (
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
		"UDP Endpoints",
	})

	for _, peer := range peers {
		tw.AppendRow(table.Row{
			peer.Hostname,
			peer.IPv4,
			peer.IPv6,
			peer.Mode,
			peer.NAT,
			strings.Join(peer.Addrs, ","),
		})
	}

	tw.SetStyle(table.Style{Box: table.StyleBoxLight})
	fmt.Println(tw.Render())
	return nil
}
