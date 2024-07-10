# p2p library

### Example
### Peer1 (act as echo server)
```go
peermapURL := "wss://synf.in/pg"

intent, err := network.JoinOIDC(oidc.ProviderGoogle, peermapURL)
if err != nil {
    panic(err)
}
fmt.Println(intent.AuthURL()) // https://synf.in/oidc/google?state=5G68CtYnMRMdrtrRF
networkSecret, err := intent.Wait(context.TODO())
if err != nil {
    panic(err)
}

pmServer, err := peer.NewPeermapURL(peermapURL, networkSecret)
if err != nil {
    panic(err)
}

// peerID is a unique string (less than 256bytes)
packetConn, err := p2p.ListenPacket(pmServer, p2p.ListenPeerID("uniqueString"))
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

### Peer2 
```go
...

packetConn, err := p2p.ListenPacket(pmServer)
if err != nil {
    panic(err)
}

defer packetConn.Close()

// "uniqueString" is above echo server's address
_, err := packetConn.WriteTo(peer.ID("uniqueString"), []byte("hello"))
if err != nil {
    panic(err)
}

packetConn.SetReadDeadline(time.Now().Add(time.Second))

buf := make([]byte, 1024)
n, peerID, err := packetConn.ReadFrom(buf)
if err !=nil {
    panic(err)
}
fmt.Println(peerID, ":", string(buf[:n])) // uniqueString : hello
```
