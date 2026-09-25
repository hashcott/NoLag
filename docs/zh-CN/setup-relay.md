# 部署中继

[English](../en/setup-relay.md) · [Tiếng Việt](../vi/setup-relay.md) · **简体中文**

中继（relay）是一台把玩家的游戏流量转发到游戏服务器的 VPS。它由社区成员（即*贡献者*，contributor）
提供，贡献者会因此获得一个贡献者密钥（key）。中继转发的任何流量都仅限于已发布的游戏地址段，
并按玩家限速，因此无法被当作通用代理使用。

## 要求

| | |
|---|---|
| 虚拟化 | **KVM**。OpenVZ 和 LXC 无法创建 TUN 设备；若缺少 `/dev/net/tun`，安装程序会中止 |
| 内核 | 5.6 或更高版本（WireGuard 自 5.6 起内置于内核） |
| 操作系统 | 使用 systemd 且带有 `apt-get`、`dnf` 或 `yum` 的 Linux（Debian、Ubuntu、Fedora、RHEL 系） |
| 位置 | 靠近游戏服务器：首选新加坡，其次东京 |
| 规格 | 最小规格的实例即可。一局游戏大约使用 10 KB/s |
| 网络 | 一个公网 IPv4 地址，并在服务商防火墙中开放 UDP 51820 |
| 需从运维者处获得 | 贡献者密钥 `GNL-XXXX-XXXX-XXXX-XXXX` 和控制平面（control plane）URL |

## 1. 获取安装程序和 agent

将 `relay-v1.sh` 和 `gnl-agent` 放在同一目录下。它们来自代码仓库（`deploy/relay-v1.sh`）
和 CI 构件 `gamenolag-linux-amd64`，或来自运维者发布的、附带 `.sha256` 文件的版本：

```bash
sha256sum -c relay-v1.sh.sha256 gnl-agent.sha256
less relay-v1.sh          # it runs as root: read it first
chmod +x relay-v1.sh gnl-agent
```

自行构建 `gnl-agent`：
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gnl-agent ./cmd/gnl-agent`。

## 2. 安装

```bash
sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX \
                   --control https://cp.example.com \
                   --region sgp
```

| 选项 | 默认值 | 含义 |
|---|---|---|
| `--key` | — | 贡献者密钥。必填 |
| `--control` | — | 控制平面 URL，仅限 `https`。必填 |
| `--region` | — | 显示给运维者的自由文本标签，例如 `sgp`、`tyo` |
| `--endpoint IP` | WAN 地址 | 玩家连接的公网地址。服务商对 VPS 做了 NAT 时需要设置 |
| `--port N` | `51820` | WireGuard UDP 端口 |
| `--iface NAME` | `wg0` | WireGuard 网络接口名 |
| `--rate RATE` | `64kb/s` | 每个会话在每个方向上的速率上限 |
| `--uninstall` | — | 移除该脚本安装的所有内容 |

它会对机器做以下改动：

- 若缺少 `wireguard-tools`、`ipset` 和 `iptables`，则安装它们。
- 创建 WireGuard 网络接口，以及私钥 `/etc/gnl/relay.key`（权限 `0600`）。该密钥永远不会离开本机。
- 设置 `net.ipv4.ip_forward=1` 和 `rp_filter=2`。
- 添加五条 iptables 规则和一个 ipset（`gnl-games`）。
- 安装 `/usr/local/bin/gnl-agent` 和两个 systemd 单元：
  - `gnl-wg.service` 在每次启动时重建网络接口。
  - `gnl-agent.service` 每 10 秒与控制平面同步一次。

如果安装程序因 WAN 接口是私有地址而中止，说明服务商对该 VPS 做了 NAT。
请加上 `--endpoint <public-ip>` 重新运行。以私有地址注册的中继永远收不到握手（handshake），
而且它自己的日志里不会有任何提示说明原因。

## 3. 在服务商处开放 UDP 端口

在服务商的控制面板中，为该 VPS 的安全组（security group）或防火墙开放 **UDP 51820**
（或你设置的 `--port`）。安装程序无法从机器内部检查这一点，而这正是中继在本地看起来一切正常、
但从其他任何地方都无法访问的最常见原因。

## 4. 本地检查

```bash
systemctl status gnl-wg gnl-agent
wg show                          # interface, port, peers
journalctl -u gnl-agent -n 20    # a sync line every 10 s
iptables -L FORWARD -n -v --line-numbers | head
ipset list gnl-games             # empty until a game profile is published
```

两条 `hashlimit` DROP 规则必须位于 `FORWARD` 中 ACCEPT 规则的**上方**；
放在下方则永远不会被匹配。FORWARD 策略必须为 `DROP`。

重启一次，然后再次检查两个单元。WireGuard 网络接口本身不会在重启后保留；由 `gnl-wg` 负责重建。

<a id="5-verify-from-outside"></a>
## 5. 从外部验证

在**另一台机器**上执行，而不是在中继本机上（需要 root 权限来创建临时网络接口）：

```bash
sudo gnl-relaycheck -endpoint <relay-ip>:51820 \
                    -pubkey "$(ssh relay 'sudo wg pubkey < /etc/gnl/relay.key')" \
                    -control https://cp.example.com -key GNL-XXXX-XXXX-XXXX-XXXX
```

- `REACHABLE`：中继被标记为 `up`。
- `NOT REACHABLE`：打印可能的原因，首先是服务商的防火墙。

`-control` 和 `-key` 负责记录检查结论。不带它们时，你能看到结果，但控制平面永远不会得知，
中继会一直保持 `pending`。密钥用于证明该报告来自中继的所有者，因此没有人能把陌生人的中继踢下线。

新通过验证的中继，要在其观察期（自注册起 48 小时）结束后才会提供给玩家。
在任何可能改变入站路径的操作之后——重启、防火墙变更、更换 IP 地址——请重新运行该检查。

## 贡献者受到的保护

- **出站白名单（egress allowlist）。** 转发的流量只能发往 `gnl-games` 中已发布的游戏地址段，
  其他一律丢弃。
- **速率上限。** 每个会话每个方向 64 KB/s，约为正常游戏流量的六倍。这是速率上限，不是月度流量配额；
  与白名单一起，使总用量保持在很低的水平。
- **没有入站服务。** 只使用 WireGuard 的 UDP 端口。
- **吊销立即生效。** 在控制平面上移除一个对端（peer），会在一次同步内将其从内核中移除。

## 卸载

```bash
sudo ./relay-v1.sh --uninstall
```

它会移除 systemd 单元、网络接口、二进制文件、防火墙规则、ipset 和 sysctl 文件，
并恢复之前的 FORWARD 策略。它会有意**保留 `/etc/gnl`**，其中存放着中继的私钥。
用完后请手动删除（`sudo rm -rf /etc/gnl`），并请运维者删除该中继的记录。

## 故障排查

| 现象 | 原因 | 解决方法 |
|---|---|---|
| 安装程序：`/dev/net/tun is missing` | OpenVZ 或 LXC | 使用 KVM VPS |
| 安装程序：内核版本低于 5.6 | 镜像过旧 | 升级内核或选择更新的镜像 |
| 安装程序拒绝私有 WAN 地址 | 服务商 NAT | `--endpoint <public-ip>` |
| `gnl-relaycheck`：NOT REACHABLE | 服务商防火墙未开放 | 在服务商处开放 UDP 51820 |
| 中继一直处于 `pending` | 检查结果从未上报 | 带上 `-control` 和 `-key` 重新运行 `gnl-relaycheck` |
| 中继变为 `down` | 5 分钟内没有同步 | `systemctl status gnl-agent`、`journalctl -u gnl-agent` |
| 重启后 `gnl-wg` 失败 | 缺少 `/etc/gnl/relay.state` 或 `relay.key` | 重新运行安装程序 |

关于每一步背后的完整理由，请参见 [P1 运行手册](../p1-runbook.md)（英文）。
