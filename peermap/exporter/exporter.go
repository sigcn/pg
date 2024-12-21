package exporter

type NetworkHead struct {
	ID         string `json:"n"`
	Alias      string `json:"n1,omitempty"`
	PeersCount int    `json:"c,omitempty"`
	CreateTime string `json:"t"`
}

type Network struct {
	ID    string   `json:"n"`
	Alias string   `json:"n1,omitempty"`
	Peers []string `json:"p,omitempty"`
}

type NetworkMeta struct {
	Alias     string   `json:"alias"`
	Neighbors []string `json:"neighbors"`
}
