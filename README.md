## Example

### Code
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

packetConn, err := p2p.ListenPacket(secret.Secret, peermap)
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

### VPN
```
peerguard vpn --peermap wss://synf.in/pg --cidr 100.1.1.1/24
```
