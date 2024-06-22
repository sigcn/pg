package exporter

type NetworkHead struct {
	ID         string `json:"n"`
	Alias      string `json:"n1"`
	PeersCount int    `json:"c"`
	CreateTime string `json:"t"`
}

type Network struct {
	ID    string   `json:"n"`
	Alias string   `json:"n1"`
	Peers []string `json:"p"`
}

type NetworkMeta struct {
	Alias     string   `json:"alias"`
	Neighbors []string `json:"neighbors"`
}
