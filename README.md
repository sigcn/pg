### Example
```go
peermap := "wss://synf.in/pg"

intent, err := network.JoinOIDC(peermap, "google")
if err != nil {
    panic(err)
}
fmt.Println(intent.AuthURL()) // https://synf.in/oidc/google?state=5G68CtYnMRMdrtrRF
secret, err := intent.Wait(context.TODO())
if err != nil {
    panic(err)
}

packetConn, err := p2p.ListenPacket(
    p2p.NetworkSecret(secret.Secret),
    p2p.Peermap(peermap),
)
if err != nil {
    panic(err)
}

// unreliability echo server
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