## Example

### 1. Code

#### Peer1 (act as echo server)
```go
peermap := p2p.Peermap("wss://synf.in/pg")

intent, err := network.JoinOIDC(oidc.ProviderGoogle, peermap)
if err != nil {
    panic(err)
}
fmt.Println(intent.AuthURL()) // https://synf.in/oidc/google?state=5G68CtYnMRMdrtrRF
secret, err := intent.Wait(context.TODO())
if err != nil {
    panic(err)
}

packetConn, err := p2p.ListenPacket(
    secret.Secret, peermap, 
    p2p.ListenPeerID("uniqueString"), // any unique string (less than 256bytes)
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

#### Peer2 
```go
...

packetConn, err := p2p.ListenPacket(secret.Secret, peermap)
if err != nil {
    panic(err)
}

defer packetConn.Close()

// "uniqueString" is above peer's address
_, err := packetConn.WriteTo(peer.PeerID("uniqueString"), []byte("hello"))
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

### 2. VPN
```
peerguard vpn --peermap wss://synf.in/pg --cidr 100.1.1.1/24
```
