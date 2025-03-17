# p2p library

### Example

### Peer1 (act as echo server)

```go
serverURL := "wss://openpg.in/pg"

intent := langs.Must(network.JoinOIDC(oidc.ProviderGoogle, peermapURL))
fmt.Println(intent.AuthURL()) // https://openpg.in/oidc/google?state=5G68CtYnMRMdrtrRF
networkSecret := langs.Must(intent.Wait(context.TODO()))

// peerID is a unique string (less than 256bytes)
packetConn := langs.Must(p2p.ListenPacket(
    disco.NewServer(serverURL, networkSecret),
    p2p.ListenPeerID("uniqueString"),
))

// unreliability echo server
buf := make([]byte, 1024)
for {
    n, peerID, err := packetConn.ReadFrom(buf)
    if err != nil {
        panic(err)
    }
    fmt.Println("Echo packet to", peerID, string(buf[:n]))
    langs.Must(packetConn.WriteTo(peerID, buf[:n]))
}
```

### Peer2

```go
...

packetConn := langs.Must(p2p.ListenPacket(disco.NewServer(serverURL, networkSecret)))
defer packetConn.Close()

// "uniqueString" is above echo server's address
langs.Must(packetConn.WriteTo(peer.ID("uniqueString"), []byte("hello")))

packetConn.SetReadDeadline(time.Now().Add(time.Second))

buf := make([]byte, 1024)
n, peerID, err := packetConn.ReadFrom(buf)
if err !=nil {
    panic(err)
}
fmt.Println(peerID, ":", string(buf[:n])) // uniqueString : hello
```
