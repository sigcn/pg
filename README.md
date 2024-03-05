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
    &secret, peermap, 
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

// "uniqueString" is above echo server's address
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

### 2. VPN(p2p)

#### Machine 1
```
# peerguard vpn --peermap wss://synf.in/pg --ipv4 100.64.0.1/24 --ipv6 fd00::1/64
```

#### Machine 2
```
# peerguard vpn --peermap wss://synf.in/pg --ipv4 100.64.0.2/24 --ipv6 fd00::2/64
```
**Another terminal on machine 2**
```
# ping 100.64.0.1
PING 100.64.0.1 (100.64.0.1) 56(84) bytes of data.
64 bytes from 100.64.0.1: icmp_seq=1 ttl=64 time=7.88 ms
64 bytes from 100.64.0.1: icmp_seq=2 ttl=64 time=4.19 ms
64 bytes from 100.64.0.1: icmp_seq=3 ttl=64 time=4.47 ms
64 bytes from 100.64.0.1: icmp_seq=4 ttl=64 time=4.54 ms
...
# ping fd00::1
PING fd00::1 (fd00::1) 56 data bytes
64 bytes from fd00::1: icmp_seq=1 ttl=64 time=4.29 ms
64 bytes from fd00::1: icmp_seq=2 ttl=64 time=5.84 ms
64 bytes from fd00::1: icmp_seq=3 ttl=64 time=3.48 ms
64 bytes from fd00::1: icmp_seq=4 ttl=64 time=4.69 ms
```
