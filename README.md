### Example
```go
packetConn, err := p2p.ListenPacket(
    p2p.NetworkSecret("8EBTbAcAEKfMUjWKnrhQgC7mmFXrkVhWzMNs8P7h6tXsxBhxB9VnncTScXyaw22JkZ"),
    p2p.Peermap("wss://synf.in/pg"),
)
if err != nil {
    panic(err)
}

buf := make([]byte, 1024) 
for {
    n, peerID, err := packetConn.ReadFrom(buf)
    if err != nil {
        panic(err)
    }
    fmt.Println("Echo packet to", peerID, string(buf[:n]))
    _, err = packetConn.WriteTo(peerID, buf[:n])
    if err != nil {
        panic(err)
    }
}
```