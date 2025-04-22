package client

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/sigcn/pg/cmd/pgcli/vpn/ipc/sdk"
	"github.com/sigcn/pg/disco"
)

func PrintNodeInfo() error {
	nodeInfo, err := (&sdk.ApiClient{}).QueryNodeInfo()
	if err != nil {
		return err
	}

	flags := parseFlags(nodeInfo.Meta["label"])

	tw := table.NewWriter()

	var addrs []string
	for _, addr := range nodeInfo.NATInfo.Addrs {
		addrs = append(addrs, addr.String())
	}

	tw.AppendRows([]table.Row{
		{"ID", nodeInfo.ID},
		{"Name", cmp.Or(nodeInfo.Meta.Get("name"), "-")},
		{"IPv4", cmp.Or(nodeInfo.Meta.Get("alias1"), "-")},
		{"IPv6", cmp.Or(nodeInfo.Meta.Get("alias2"), "-")},
		{"NAT", cmp.Or(nodeInfo.NATInfo.Type, "-")},
		{"Flags", cmp.Or(strings.Join(flags, ","), "-")},
		{"Endpoints", cmp.Or(strings.Join(addrs[:min(len(addrs), 3)], ","), "-")},
		{"Version", nodeInfo.Version},
	})
	tw.SetStyle(table.Style{Box: table.StyleBoxLight})
	fmt.Println(tw.Render())
	return nil
}

func PrintPeers() error {
	peers, err := (&sdk.ApiClient{}).QueryPeers()
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
		"Endpoints",
		"Version",
	})

	for _, peer := range peers {
		if _, ok := peer.Labels.Get("node.off"); ok {
			peer.Mode = ""
		}
		tw.AppendRow(table.Row{
			cmp.Or(peer.Hostname, "-"),
			cmp.Or(peer.IPv4, "-"),
			cmp.Or(peer.IPv6, "-"),
			cmp.Or(peer.Mode, "-"),
			cmp.Or(peer.NAT, "-"),
			cmp.Or(strings.Join(parseFlags(peer.Labels), ","), "-"),
			cmp.Or(strings.Join(peer.Addrs[:min(len(peer.Addrs), 3)], ","), "-"),
			peer.Version,
		})
	}

	tw.SetStyle(table.Style{Box: table.StyleBoxLight})
	fmt.Println(tw.Render())
	return nil
}

func parseFlags(labels disco.Labels) []string {
	var flags []string
	if _, ok := labels.Get("node.nr"); ok {
		flags = append(flags, "NR")
	}
	if _, ok := labels.Get("node.off"); ok {
		flags = append(flags, "OFF")
	}
	return flags
}
