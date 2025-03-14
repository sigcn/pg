package vpn

import (
	"bytes"
	"log/slog"
	"net"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

// ICMPHostUnreachable build a icmp(6) packet
//
// icmp-host-unreachable for ipv4
// icmp6-addr-unreachable for ipv6
func ICMPHostUnreachable(srcIP, dstIP net.IP, data []byte) []byte {
	var packetLayers []gopacket.SerializableLayer

	if srcIP.To4() != nil { // ipv4
		packetLayers = append(packetLayers, &layers.IPv4{
			Version:  4,
			IHL:      5,
			SrcIP:    srcIP,
			DstIP:    dstIP,
			TTL:      64,
			Protocol: layers.IPProtocolICMPv4,
		})
		packetLayers = append(packetLayers, &layers.ICMPv4{
			TypeCode: layers.CreateICMPv4TypeCode(layers.ICMPv4TypeDestinationUnreachable, layers.ICMPv4CodeHost),
		})
	} else { // ipv6
		ipLayer := &layers.IPv6{
			Version:    6,
			SrcIP:      srcIP,
			DstIP:      dstIP,
			HopLimit:   64,
			NextHeader: layers.IPProtocolICMPv6,
		}
		icmpv6Layer := &layers.ICMPv6{
			TypeCode: layers.CreateICMPv6TypeCode(layers.ICMPv6TypeDestinationUnreachable, layers.ICMPv6CodeAddressUnreachable),
		}
		_ = icmpv6Layer.SetNetworkLayerForChecksum(ipLayer)
		packetLayers = append(packetLayers, ipLayer)
		packetLayers = append(packetLayers, icmpv6Layer)
		packetLayers = append(packetLayers, gopacket.Payload(bytes.Repeat([]byte{0}, 4))) // 4 bytes reserved
	}

	packetLayers = append(packetLayers, gopacket.Payload(data))

	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, packetLayers...); err != nil {
		slog.Error("Serialize icmp-host-unreachable", "err", err)
		return nil
	}

	return buffer.Bytes()
}
