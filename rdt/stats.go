package rdt

import (
	"encoding/json"
	"net"
	"net/http"
)

type Stat struct {
	RemoteAddr string `json:"remoteAddr"`
	RecvNO     uint32 `json:"recvNO"`
	SentNO     uint32 `json:"sentNO"`
	ACKNO      uint32 `json:"ackNO"`
	State      int    `json:"state"`
	RecvPool   int    `json:"recvPool"`
	SendPool   int    `json:"sendPool"`
	Resend     uint32 `json:"resend"`
}

func runStatsHTTPServer(statsListener net.Listener, l *RDTListener) {
	http.HandleFunc("/stat", func(w http.ResponseWriter, r *http.Request) {
		var acceptStats []Stat
		for k, v := range l.acceptConnMap {
			acceptStats = append(acceptStats, Stat{
				RemoteAddr: k,
				RecvNO:     v.recvNO,
				SentNO:     v.sentNO,
				ACKNO:      v.ackNO,
				State:      int(v.state.Load()),
				RecvPool:   len(v.recvPool),
				SendPool:   len(v.sendPool),
				Resend:     v.rs.Load(),
			})
		}
		var openStats []Stat
		for k, v := range l.openConnMap {
			openStats = append(openStats, Stat{
				RemoteAddr: k,
				RecvNO:     v.recvNO,
				SentNO:     v.sentNO,
				ACKNO:      v.ackNO,
				State:      int(v.state.Load()),
				RecvPool:   len(v.recvPool),
				SendPool:   len(v.sendPool),
				Resend:     v.rs.Load(),
			})
		}
		json.NewEncoder(w).Encode(map[string][]Stat{"accept": acceptStats, "open": openStats})
	})
	go http.Serve(statsListener, nil)
}
