<p align="center">
    <img src="peermap/ui/src/assets/logo.png" width="180" />
</p>

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
> Time synchronization between nodes is crucial; the difference should not exceed 10 seconds

```sh
# node1
pgcli vpn -s wss://openpg.in/pg -4 100.64.0.1/24
```

```sh
# node2
pgcli vpn -s wss://openpg.in/pg -4 100.64.0.2/24
```

## Advanced

### Self-hosted peermap server

#### 1. run the pgmap daemon

```sh
$ pgmap -l 127.0.0.1:9987 --secret-key 5172554832d76672d1959a5ac63c5ab9 \
    --stun 111.206.174.2:3478 --stun 106.13.249.54:3478 --stun 106.12.251.52:3478 --stun 106.12.251.31:3478
```

#### 2. wrap pgmap as an https server

```sh
$ caddy reverse-proxy --from https://openpg.in --to 127.0.0.1:9987
```

### P2P file sharing

```sh
# share
$ pgcli share -s wss://openpg.in/pg ~/my-show.pptx
ShareURL: pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```

```sh
# download
$ pgcli download -s wss://openpg.in/pg pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```

### Shortcut pgvpn

```sh
ln -sf /usr/sbin/pgcli /usr/sbin/pgvpn
```

You can now use `pgvpn` instead of `pgcli vpn`.

### Use IPC to query the found peers.

```sh
pgvpn --peers
```

### Rootless mode VPN

```sh
pgvpn -s wss://openpg.in/pg -4 100.64.0.1/24 --proxy-listen 127.0.0.1:4090 --forward tcp://127.0.0.1:80 --forward udp://8.8.8.8:53
```

### Uses pre-shared secret file instead of OIDC auth

**first**

```sh
$ export PG_SECRET_KEY=5172554832d76672d1959a5ac63c5ab9
$ export PG_SERVER=wss://openpg.in/pg
$ pgcli admin secret --network "<email>" --duration 24h > psns.json
```

**then**

```sh
sudo pgcli vpn -s wss://openpg.in/pg -4 100.64.0.1/24 -f psns.json
```

## License

[GNU General Public License v3.0](https://github.com/sigcn/pg/blob/main/LICENSE)

## Contributing

Contributions welcome! Have an improvement? Submit a pull request.

> [!NOTE]
> I also maintain a closed-source version, and contributions to the open-source project may be included in the closed-source version.

## Contact

- Telegram Group: https://t.me/+-S5L6ZCBxlxkMTRl
- QQ Group: 1039776116
