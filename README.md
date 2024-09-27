# PeerGuard

Another p2p network library in Go. Committed to direct communication between devices.  
[[简体中文]](https://github.com/sigcn/pg/blob/main/README_zh_CN.md)
## Features
- Elegantly simple architecture (pgcli & pgmap & OpenID Connect)
- NAT traversal with high success rate (STUN & UPnP & PortScan & BirthdayParadox)
- Full support for IPv4/IPv6 dual stack
- Easy-to-use library (net.PacketConn) 
- **Transport layer security (curve25519 & chacha20poly1305 for end-to-end encryption)**
- [RDT](https://github.com/sigcn/pg/tree/main/rdt) protocol for reliable data transfer  
- Cross-platform compatibility (linux/windows/macOS/iOS/android)

## Get Started
> [!NOTE]
> Time synchronization between nodes is crucial; the difference should not exceed 5 seconds
### p2p vpn
```sh
# node1
sudo pgcli vpn -s wss://synf.in/pg --ipv4 100.64.0.1/24 --ipv6 fd00::1/64
```
```sh
# node2
sudo pgcli vpn -s wss://synf.in/pg --ipv4 100.64.0.2/24 --ipv6 fd00::2/64
```
### p2p file sharing
```sh
# share
$ PG_SERVER=wss://synf.in/pg pgcli share ~/my-show.pptx
ShareURL: pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```
```sh
# download
$ PG_SERVER=wss://synf.in/pg pgcli download pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```

## Advanced
### deploy the peermap server
#### 1. run the pgmap daemon
```sh
$ pgmap -l 127.0.0.1:9987 --secret-key 5172554832d76672d1959a5ac63c5ab9 \
    --stun stun.miwifi.com:3478 --stun stunserver.stunprotocol.org:3478
```

#### 2. wrap pgmap as an https server
```sh
$ caddy reverse-proxy --from https://synf.in/pg --to 127.0.0.1:9987
```
### uses pre-shared secret file instead of OIDC auth 
**first**
```sh
$ export PG_SECRET_KEY=5172554832d76672d1959a5ac63c5ab9
$ export PG_SERVER=wss://synf.in/pg
$ pgcli admin secret --network "<email>" --duration 24h > psns.json
```
**then**
```sh
sudo pgcli vpn -s wss://synf.in/pg -4 100.64.0.1/24 -f psns.json
```
## License
[GNU General Public License v3.0](https://github.com/sigcn/pg/blob/main/LICENSE)

## Contributing
Contributions welcome! Have an improvement? Submit a pull request. 
> [!NOTE]
> I also maintain a closed-source version, and contributions to the open-source project may be included in the closed-source version.
