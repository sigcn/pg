package disco

import (
	"log/slog"
	"net"
)

type cidrs []*net.IPNet

func (cs *cidrs) Contains(ip net.IP) bool {
	for _, cidr := range *cs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var ignoredLocalCIDRs cidrs

func SetIgnoredLocalCIDRs(cidrs ...string) {
	for _, cidr := range cidrs {
		_, _cidr, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Debug("SetIgnoredLocalCIDRs parse cidr failed", "err", err)
			continue
		}
		ignoredLocalCIDRs = append(ignoredLocalCIDRs, _cidr)
	}
}

func ListLocalIPs() ([]net.IP, error) {
	var ips []net.IP
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addresses {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ignoredLocalCIDRs.Contains(ipnet.IP) {
				continue
			}
			ips = append(ips, ipnet.IP)
		}
	}
	return ips, nil
}
