package udp

import (
	"testing"

	"github.com/sigcn/pg/disco"
)

/*
Running tool: /usr/bin/go test -benchmem -run=^$ -bench ^BenchmarkPeers$ github.com/sigcn/pg/disco/udp

goos: linux
goarch: amd64
pkg: github.com/sigcn/pg/disco/udp
cpu: 11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz
BenchmarkPeers-8   	59405948	        19.67 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/sigcn/pg/disco/udp	1.192s
*/
func BenchmarkPeers(b *testing.B) {
	udpConn := UDPConn{
		peersIndex: map[disco.PeerID]*peerkeeper{
			"HNovweYs1okUWDnRNTLCsobtctxVbjUQ1JJJocCQUnHE": {
				states: map[string]*PeerState{
					"192.168.0.1": {},
				},
			},
			"6X4a2wgkg1ytTAyHNqSAr2aN7yeAsT57tA6wSaUQsGnH": {
				states: map[string]*PeerState{
					"192.168.0.10": {},
				},
			},
			"BxPaLdfoDvHvARyX51C9yjtQY73XY1XrKKPABSDHg1Qp": {
				states: map[string]*PeerState{
					"192.168.0.20": {},
				},
			},
			"EzmYvW7E45NgL1jwVswBaVjBubiLM5EJsqUwNZ9d4Zir": {
				states: map[string]*PeerState{
					"192.168.0.30": {},
				},
			},
			"3yzbsVw2HKiHSJDRiw99kFtaADR5vwMGR31Uz7Rpmu1G": {
				states: map[string]*PeerState{
					"192.168.0.40": {},
				},
			},
		},
	}
	for range b.N {
		udpConn.Peers()
	}
}
