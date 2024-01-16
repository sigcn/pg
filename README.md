### Example
```go
packetConn, err := p2p.ListenPacket(
    peer.NetworkID("HXncIcQ4ocsGtOOrQQiG4H2WHXPplZakuq4f6EJR1cg="),
    []string{"wss://synf.in/pg"},
)
if err != nil {
    panic(err)
}

for {
    buf := make([]byte, 1024) 
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