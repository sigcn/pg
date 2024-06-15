package exporter

type NetworkHead struct {
	ID         string `json:"n"`
	PeersCount int    `json:"c"`
	CreateTime string `json:"t"`
}

type Network struct {
	ID    string   `json:"n"`
	Peers []string `json:"p"`
}

type PutNetworkMetaRequest struct {
	Alias     string   `json:"alias"`
	Neighbors []string `json:"neighbors"`
}
