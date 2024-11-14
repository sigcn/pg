package udp

import (
	"log/slog"
	"net"

	"github.com/sigcn/pg/upnp"
)

type upnpPortMapping struct {
	upnpDeleteMapping func()
}

func (m *upnpPortMapping) mappingAddress(udpPort int) *net.UDPAddr {
	nat, err := upnp.Discover()
	if err != nil {
		slog.Debug("[UPnP] Disabled", "reason", err)
		return nil
	}
	externalIP, err := nat.GetExternalAddress()
	if err != nil {
		slog.Debug("[UPnP] Disabled", "reason", err)
		return nil
	}

	if externalIP.IsUnspecified() {
		slog.Debug("[UPnP] Disabled", "reason", "invalid external ip")
		return nil
	}

	for i := 0; i < 20; i++ {
		mappedPort, err := nat.AddPortMapping("udp", udpPort+i, udpPort, "peerguard", 24*3600)
		if err != nil {
			continue
		}
		m.upnpDeleteMapping = func() { nat.DeletePortMapping("udp", mappedPort, udpPort) }
		return &net.UDPAddr{IP: externalIP, Port: mappedPort}
	}
	return nil
}

func (m *upnpPortMapping) close() {
	if m.upnpDeleteMapping != nil {
		m.upnpDeleteMapping()
	}
}
