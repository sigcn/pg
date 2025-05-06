package disco

import (
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
)

type cidrs []net.IPNet

func (cs *cidrs) Contains(ip net.IP) bool {
	for _, cidr := range *cs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

type interfaceNamePrefixs []string

func (inp *interfaceNamePrefixs) HasPrefix(interfaceName string) bool {
	for _, ifaceNamePrefix := range *inp {
		if strings.HasPrefix(interfaceName, ifaceNamePrefix) {
			return true
		}
	}
	return false
}

var (
	ignoredLocalCIDRs                cidrs
	ignoredLocalInterfaceNamePrefixs interfaceNamePrefixs
	localIPs                         []net.IP
)

func SetIgnoredLocalCIDRs(cidrs ...string) {
	ignoredLocalCIDRs = ignoredLocalCIDRs[:0]
	AddIgnoredLocalCIDRs(cidrs...)
}

func AddIgnoredLocalCIDRs(cidrs ...string) {
	for _, cidr := range cidrs {
		_, _cidr, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Debug("SetIgnoredLocalCIDRs parse cidr failed", "err", err)
			continue
		}
		if _cidr.IP.IsUnspecified() {
			continue
		}
		slog.Debug("IgnoreLocalCIDR " + cidr)
		ignoredLocalCIDRs = append(ignoredLocalCIDRs, *_cidr)
	}
}

func RemoveIgnoredLocalCIDRs(cidrs ...string) {
	var filterd []net.IPNet
	for _, cidr := range ignoredLocalCIDRs {
		if !slices.Contains(cidrs, cidr.String()) {
			filterd = append(filterd, cidr)
		}
	}
	ignoredLocalCIDRs = filterd
}

func IsIgnoredLocalIP(ip net.IP) bool {
	return ignoredLocalCIDRs.Contains(ip)
}

func GetIgnoredLocalCIDRs() []net.IPNet {
	return slices.Clone(ignoredLocalCIDRs)
}

func SetIgnoredLocalInterfaceNamePrefixs(prefixs ...string) {
	ignoredLocalInterfaceNamePrefixs = prefixs
	for _, prefix := range prefixs {
		slog.Debug("IgnoreInterface", "prefix", prefix)
	}
}

func SetLocalIPs(ips ...net.IP) {
	localIPs = ips
}

func GetIgnoredLocalInterfaceNamePrefixs() []string {
	return slices.Clone(ignoredLocalInterfaceNamePrefixs)
}

func ListLocalIPs() ([]net.IP, error) {
	if len(localIPs) > 0 {
		return localIPs, nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagRunning != net.FlagRunning {
			continue
		}
		if ignoredLocalInterfaceNamePrefixs.HasPrefix(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ipnet.IP.IsLoopback() {
					continue
				}
				if ipnet.IP.IsLinkLocalUnicast() {
					continue
				}
				if ignoredLocalCIDRs.Contains(ipnet.IP) {
					continue
				}
				ips = append(ips, ipnet.IP)
			}
		}
	}
	return ips, nil
}

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

func IsCGN(ip net.IP) bool {
	if ip.To4() == nil {
		return false
	}
	return carrierGradeNAT.Contains(netip.AddrFrom4([4]byte(ip.To4())))
}
