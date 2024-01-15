### Example
```go
node, err := p2p.New(
    peer.NetworkID("HXncIcQ4ocsGtOOrQQiG4H2WHXPplZakuq4f6EJR1cg="),
    []string{"wss://synf.in/pg"},
)
if err != nil {
    panic(err)
}

for {
    buf := make([]byte, 1024) 
    n, peerID, err := node.PacketConn().ReadFrom(buf)
    if err != nil {
        panic(err)
    }
    fmt.Println("Echo packet to", peerID, string(buf[:n]))
    _, err = node.PacketConn().WriteTo(peerID, buf[:n])
    if err != nil {
        panic(err)
    }
}
```