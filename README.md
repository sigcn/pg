# PeerGuard

Another p2p network library in Go 

## Features
- Elegantly simple architecture (pgcli & pgserve & OpenID Connect)
- NAT traversal with high success rate (STUN & UPnP & PortScan)
- Full support for IPv4/IPv6 dual stack
- Easy-to-use library (net.PacketConn) 
- **Transport layer security (curve25519 & chacha20poly1305 for end-to-end encryption)**
- Cross-platform compatibility (linux/windows/macOS/iOS/android)

## Get Started

### Deploy the peermap server
#### 1. Run the pgserve daemon
```
$ pgserve -l 127.0.0.1:9987 --secret-key 5172554832d76672d1959a5ac63c5ab9 \
    --stun stun.qq.com:3478 --stun stun.miwifi.com:3478
```

#### 2. Wrap pgserve as an https server
```
$ caddy reverse-proxy --from https://synf.in/pg --to 127.0.0.1:9987
```

### Follow the steps below to run VPN nodes in different physical networks
#### 1. Generate a private network secret
```
$ pgcli secret --secret-key 5172554832d76672d1959a5ac63c5ab9 > ~/.peerguard_network_secret.json
```
#### 2. Run a VPN daemon
```
# pgcli vpn -s wss://synf.in/pg --ipv4 100.64.0.1/24 --ipv6 fd00::1/64
```
> [!NOTE]
> Since the default encryption algorithm has been changed from AES to ChaCha20, it is required that the local clocks of each VPN node do not differ by more than 5 seconds.
## P2P programming example
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

pmap, err := peermap.New(peermapURL, networkSecret)
if err != nil {
    panic(err)
}

packetConn, err := p2p.ListenPacket(
    pmap,
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

### Peer2 
```go
...

packetConn, err := p2p.ListenPacket(pmap)
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
