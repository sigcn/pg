# VPN
a Virtual Private Network library  
### Example
**peer1**
```go
packetConn, err := net.ListenPacket("udp", "192.168.3.99:22334")
if err != nil {
    panic(err)
}

rt := vpn.NewRoutingTable()
rt.AddPeer("10.10.10.1", "", &net.UDPAddr{IP: net.ParseIP("192.168.3.98", Port: 22335)})

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

err := vpn.New(rt, packetConn, vpn.Config{MTU: 1428, IPv4: "10.10.10.2/24"}).RunTun(ctx, "tun1")
if err != nil {
    panic(err)
}
```
**peer2**
```go
packetConn, err := net.ListenPacket("udp", "192.168.3.98:22335")
if err != nil {
    panic(err)
}

rt := vpn.NewRoutingTable()
rt.AddPeer("10.10.10.2", "", &net.UDPAddr{IP: net.ParseIP("192.168.3.99", Port: 22334)})

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer cancel()

err := vpn.New(rt, packetConn, vpn.Config{MTU: 1428, IPv4: "10.10.10.1/24"}).RunTun(ctx, "tun1")
if err != nil {
    panic(err)
}
```