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
	State      int    `json:"state"`
	RecvPool   int    `json:"recvPool"`
	SendPool   int    `json:"sendPool"`
	Resend     uint32 `json:"resend"`
}

func runStatsHTTPServer(statsListener net.Listener, l *RDTListener) {
	http.HandleFunc("/stat", func(w http.ResponseWriter, r *http.Request) {
		var stats []Stat
		for k, v := range l.connMap {
			stats = append(stats, Stat{
				RemoteAddr: k,
				RecvNO:     v.recvNO,
				SentNO:     v.sentNO,
				State:      int(v.state.Load()),
				RecvPool:   len(v.recvPool),
				SendPool:   len(v.sendPool),
				Resend:     v.rs.Load(),
			})
		}
		json.NewEncoder(w).Encode(stats)
	})
	go http.Serve(statsListener, nil)
}
