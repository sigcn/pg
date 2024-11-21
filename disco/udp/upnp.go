package udp

import (
	"errors"
	"net"

	"github.com/sigcn/pg/upnp"
)

type upnpPortMapping struct {
	upnpDeleteMapping func()
}

func (m *upnpPortMapping) mappingAddress(udpPort int) (*net.UDPAddr, error) {
	nat, err := upnp.Discover()
	if err != nil {
		return nil, err
	}
	externalIP, err := nat.GetExternalAddress()
	if err != nil {
		return nil, err
	}

	if externalIP.IsUnspecified() {
		return nil, errors.New("invalid external ip")
	}

	for i := 0; i < 20; i++ {
		mappedPort, err := nat.AddPortMapping("udp", udpPort+i, udpPort, "peerguard", 24*3600)
		if err != nil {
			continue
		}
		m.upnpDeleteMapping = func() { nat.DeletePortMapping("udp", mappedPort, udpPort) }
		return &net.UDPAddr{IP: externalIP, Port: mappedPort}, nil
	}
	return nil, errors.New("add port mapping failed")
}

func (m *upnpPortMapping) close() {
	if m.upnpDeleteMapping != nil {
		m.upnpDeleteMapping()
	}
}
