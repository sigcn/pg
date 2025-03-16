<p align="center">
    <img src="peermap/ui/src/assets/logo.png" width="180" />
</p>

# PeerGuard

另一个 Go 实现的 p2p 类库。致力于设备之间直接通信。

## 特性

- 简洁架构
- 极高的 NAT(防火墙) 穿越成功率，并支持失败时回退到 websocket
- 完全的 ipv4/ipv6 双栈支持
- 很容易入手的 API （针对开发者）
- 端到端加密
- 用于可靠数据传输的 [RDT](https://github.com/sigcn/pg/tree/main/rdt) 协议
- 跨平台

## 快速开始

> [!NOTE]
> 节点间时间同步非常重要，通常相差不能超过 10 秒

```sh
# 节点1
pgcli vpn -s wss://openpg.in/pg -4 100.64.0.1/24
```

```sh
# 节点2
pgcli vpn -s wss://openpg.in/pg -4 100.64.0.2/24
```

> [!NOTE]
> 使用`github`认证时要求帐号绑定已验证的邮箱 (https://github.com/settings/emails)

## 高级用法

### 部署 peermap 服务器

#### 1. 启动 pgmap 守护进程

```sh
$ pgmap -l 127.0.0.1:9987 --secret-key 5172554832d76672d1959a5ac63c5ab9 \
    --stun 111.206.174.2:3478 --stun 106.13.249.54:3478 --stun 106.12.251.52:3478 --stun 106.12.251.31:3478
```

> [!NOTE] >`pgmap`支持配置文件（[查看所有配置项](https://github.com/sigcn/pg/blob/main/peermap/config.go#L20)）。另外，命令行参数会覆盖配置文件参数

#### 2. 上 https 更安全

```sh
$ caddy reverse-proxy --from https://openpg.in --to 127.0.0.1:9987
```

### P2P 文件分享

```sh
# 分享
$ pgcli share -s wss://openpg.in/pg ~/my-show.pptx
ShareURL: pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```

```sh
# 下载
$ pgcli download -s wss://openpg.in/pg pg://DJX2csRurJ3DvKeh63JebVHFDqVhnFjckdVhToAAiPYf/0/my-show.pptx
```

### 快捷方式 pgvpn

```sh
ln -sf /usr/sbin/pgcli /usr/sbin/pgvpn
```

现在，你可以使用`pgvpn`来代替`pgcli vpn`了

### 使用 IPC 查询找到的 peers

```sh
pgvpn --peers
```

### 去 root 权限的 VPN

```sh
pgvpn -s wss://openpg.in/pg -4 100.64.0.1/24 --proxy-listen 127.0.0.1:4090 --forward tcp://127.0.0.1:80 --forward udp://8.8.8.8:53
```

### 使用预共享密钥文件代替 OIDC 认证

**首先**

```sh
$ export PG_SECRET_KEY=5172554832d76672d1959a5ac63c5ab9
$ export PG_SERVER=wss://openpg.in/pg
$ pgcli admin secret --network "<email>" --duration 24h > psns.json
```

**然后**

```sh
sudo pgcli vpn -s wss://openpg.in/pg -4 100.64.0.1/24 -f psns.json
```

## 许可证

[GNU General Public License v3.0](https://github.com/sigcn/pg/blob/main/LICENSE)

## 参与贡献

非常欢迎参与项目的开发，如果有任何改善本项目的意图，请立即提交 PR

> [!NOTE]
> 我维护了一个功能更强的专业版本，是闭源的。任何贡献在开源版本的代码都可能被闭源版本采用，如果介意请不要参与贡献。

## 交流

- Telegram 群组: https://t.me/+-S5L6ZCBxlxkMTRl
- QQ 群组: 1039776116
