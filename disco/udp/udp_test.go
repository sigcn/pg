package udp

import (
	"net"
	"testing"
	"time"

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

/*
Running tool: /usr/bin/go test -benchmem -run=^$ -bench ^BenchmarkFindPeerID$ github.com/sigcn/pg/disco/udp

goos: linux
goarch: amd64
pkg: github.com/sigcn/pg/disco/udp
cpu: 11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz
BenchmarkFindPeerID-8   	 8232618	       154.5 ns/op	      45 B/op	       3 allocs/op
PASS
ok  	github.com/sigcn/pg/disco/udp	1.423s
*/
func BenchmarkFindPeerID(b *testing.B) {
	udpConn := UDPConn{
		peersIndex: map[disco.PeerID]*peerkeeper{
			"HNovweYs1okUWDnRNTLCsobtctxVbjUQ1JJJocCQUnHE": {
				states: map[string]*PeerState{
					"192.168.0.1": {
						PeerID:         "HNovweYs1okUWDnRNTLCsobtctxVbjUQ1JJJocCQUnHE",
						Addr:           &net.UDPAddr{IP: net.ParseIP("192.168.0.1"), Port: 12345},
						LastActiveTime: time.Now(),
					},
				},
			},
			"6X4a2wgkg1ytTAyHNqSAr2aN7yeAsT57tA6wSaUQsGnH": {
				states: map[string]*PeerState{
					"192.168.0.10": {
						PeerID:         "6X4a2wgkg1ytTAyHNqSAr2aN7yeAsT57tA6wSaUQsGnH",
						Addr:           &net.UDPAddr{IP: net.ParseIP("192.168.0.10"), Port: 12345},
						LastActiveTime: time.Now(),
					},
				},
			},
			"BxPaLdfoDvHvARyX51C9yjtQY73XY1XrKKPABSDHg1Qp": {
				states: map[string]*PeerState{
					"192.168.0.20": {
						PeerID:         "BxPaLdfoDvHvARyX51C9yjtQY73XY1XrKKPABSDHg1Qp",
						Addr:           &net.UDPAddr{IP: net.ParseIP("192.168.0.20"), Port: 12345},
						LastActiveTime: time.Now(),
					},
				},
			},
			"EzmYvW7E45NgL1jwVswBaVjBubiLM5EJsqUwNZ9d4Zir": {
				states: map[string]*PeerState{
					"192.168.0.30": {
						PeerID:         "EzmYvW7E45NgL1jwVswBaVjBubiLM5EJsqUwNZ9d4Zir",
						Addr:           &net.UDPAddr{IP: net.ParseIP("192.168.0.30"), Port: 12345},
						LastActiveTime: time.Now(),
					},
				},
			},
			"3yzbsVw2HKiHSJDRiw99kFtaADR5vwMGR31Uz7Rpmu1G": {
				states: map[string]*PeerState{
					"192.168.0.40": {
						PeerID:         "3yzbsVw2HKiHSJDRiw99kFtaADR5vwMGR31Uz7Rpmu1G",
						Addr:           &net.UDPAddr{IP: net.ParseIP("192.168.0.40"), Port: 12345},
						LastActiveTime: time.Now(),
					},
				},
			},
		},
	}
	for range b.N {
		udpConn.findPeerID(&net.UDPAddr{IP: net.ParseIP("192.168.0.40"), Port: 12345})
	}
}
