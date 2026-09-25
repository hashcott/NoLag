# 架构

[English](../en/architecture.md) · [Tiếng Việt](../vi/architecture.md) · **简体中文**

GameNoLag 由三部分组成：玩家 Windows 电脑上的**客户端**、社区贡献的 VPS 上的**中继**（relay），
以及一个知道谁可以使用哪个中继的**控制平面**（control plane）。游戏流量的路径是
客户端 → 中继 → 游戏服务器。控制平面永远不在这条路径上。

```
 Player's PC (Windows)                     Contributed VPS                Game servers
┌─────────────────────────────┐          ┌──────────────────┐        ┌──────────────┐
│ gnl-ui  (tray + window,     │          │ WireGuard (wg0)  │        │ AWS / Azure  │
│          no privilege)      │          │ gnl-agent        │        │ Singapore,   │
│    │ named pipe, 4 verbs    │  UDP     │  - peers         │        │ Tokyo        │
│    ▼                        │ ═══════► │  - egress        │ ─────► │              │
│ gnl-service (LocalSystem)   │ WireGuard│    allowlist     │        │              │
│  - measures every relay     │          │  - rate limit    │        │              │
│  - routes only game ranges  │          └────────┬─────────┘        └──────────────┘
└────────────┬────────────────┘                   │ sync every 10 s
             │ HTTPS: session, profile            │
             ▼                                    ▼
        ┌──────────────────────────────────────────────┐
        │ gnl-control + Postgres                       │
        │ keys, devices, relays, published game ranges │
        └──────────────────────────────────────────────┘
```

## 参与者

| 参与者 | 拥有 | 职责 |
|---|---|---|
| **运维者**（operator） | 控制平面及其数据库 | 签发贡献者密钥（key）、验证中继、发布游戏配置（profile） |
| **贡献者**（contributor） | 一台 KVM VPS 和一个贡献者密钥 | 运行中继；在自己最多三台电脑上使用该密钥 |
| **玩家**（player） | 一台装有客户端的 Windows 电脑 | 玩游戏；其余工作由客户端完成 |

在当前阶段，每个玩家都是贡献者：注册中继所用的密钥，就是用来激活设备的密钥。

## 组件

| 二进制 | 运行位置 | 角色 |
|---|---|---|
| `gnl-control` | Linux，置于 TLS 之后 | HTTP API 和 Postgres 存储：密钥、设备、中继、对端绑定、游戏配置 |
| `gnl-agent` | 每个中继 | 每 10 秒：上报状态，接收对端（peer）和游戏地址段，使 WireGuard、ipset 和 iptables 与之保持一致 |
| `gnl-relaycheck` | 中继之外的任意机器 | 从互联网发起一次真实的 WireGuard 握手（handshake）。这是唯一能将中继标记为 `up` 的途径 |
| `gnl-profile` | 运维者的机器 | 根据贡献者的观测，并与云服务商公布的地址段交叉核验，构建某款游戏的 CIDR 列表 |
| `gnl-service` | Windows，LocalSystem | 持有隧道：会话、测量、路由、游戏检测、故障切换 |
| `gnl-ui` | Windows，以当前用户身份 | 系统托盘图标和窗口。无特权 |
| `gnl-probe`、`gnl-analyze` | 测量主机 | P0 测量，用于判断高峰时段是否有 VPS 线路优于 ISP |

## 控制平面 API

所有端点均为基于 HTTPS 的 JSON。凭据为 bearer 令牌（token）。

| 端点 | 调用方 | 用途 |
|---|---|---|
| `POST /v1/relay/register` | 中继安装程序，携带贡献者密钥 | 注册中继；返回其 id、令牌和内部子网 |
| `POST /v1/relay/sync` | `gnl-agent`，携带其中继令牌 | 上报状态；接收对端、游戏 CIDR 和轮询间隔 |
| `POST /v1/relay/reachability` | `gnl-relaycheck`，携带所有者的密钥 | 记录该中继是否可从外部访问 |
| `POST /v1/activate` | 客户端，携带贡献者密钥 | 将本设备的公钥绑定到一个名额 |
| `GET /v1/session` | 客户端 | 提供给本设备的中继：`up` 且已过 `trusted_after` |
| `GET /v1/profile` | 客户端 | 最新发布的游戏 CIDR 列表 |
| `DELETE /v1/devices/{id}` | 贡献者 | 释放一个设备名额（device slot） |
| `POST /v1/observations` | 贡献者工具 | 观测到承载某款游戏流量的目标地址 |
| `GET /healthz` | 监控 | 存活检查 |

使用密钥的端点有限速（rate limit）：按来源地址和按密钥前缀，每小时各 20 次请求。

## 中继生命周期

```
register ──► pending ──(gnl-relaycheck: reachable)──► up ──(no sync for 5 min)──► down
                │                                     │                           │
                └──(gnl-relaycheck: not reachable)──► unreachable ◄───────────────┘
                                                      (a later check can move it back to up)
```

只有当中继处于 `up` 状态，**并且**其 `trusted_after` 时间已过时，才会提供给玩家。
同步只能证明中继能访问控制平面，并不能证明玩家能访问中继：服务商的安全组（security group）
位于 UDP 端口之前，从机器内部无法看到。只有外部握手才能确定这一点。

## 一次连接的步骤

1. 客户端获取配置（游戏 CIDR）。如果控制平面宕机，但本地已有一份配置，就继续使用那份配置。
2. 获取会话。返回 `403` 表示设备尚未激活，于是客户端先激活，再重新请求。
3. 读取当前默认路由。这一步发生在隧道网卡创建之前，因此不会把隧道误认为默认路由。
4. 将每个中继的端点一次性解析为字面 IPv4 地址。
5. 创建一个 Wintun 网卡，把每个中继添加为不带 allowed-ips 的 WireGuard 对端，
   并与每个中继握手。握手本身就是测量，而且是通过玩家自己的网络路径进行的。
6. 选出最快的中继，并将该中继的 `/32` 固定走物理网卡，使隧道自身的数据包永远不会进入隧道。
   游戏路由只在已知游戏进程运行期间安装。

此后：
- 每五分钟重新排序一次，但只在两局游戏之间进行，并且只有在至少提升 10 ms 时才切换。
- 150 秒内没有握手的中继视为已失效。客户端会切换到另一个中继；如果都无响应，则移除自己的路由。

## 游戏配置

路由按目标地址进行，因此配置必须说明哪些地址属于某款游戏，而且范围必须足够窄。
云服务商公布的是 /17 和 /18 地址块；路由整个地址块会把无关的服务也拖进中继。

```
contributors report dst ip:port ──► observed_address (per game, per key)
                                          │  ≥ 3 independent keys
                                          ▼
                                    candidates ──► cross-check against AWS / Azure / ASN ranges
                                                        │  match        │  no match
                                                        ▼               ▼
                                             widen to ≤ /20, ≥ /24   "unverified": never added
                                             cap 131 072 addresses
                                                        │
                                                        ▼
                                             gnl-profile -publish ──► game_profile vN
```

像 AWS Global Accelerator 这样的任播（anycast）地址块，只保留单个 `/32`，从不扩展。
该地址块无法说明背后是谁。

## 安全边界

| 边界 | 防护 |
|---|---|
| 非特权 UI → LocalSystem 服务 | 命名管道只对 SYSTEM、Administrators 和 INTERACTIVE 用户开放。恰好四个动词，不带参数，行长度有上限 |
| 磁盘上的服务二进制和状态 | `Program Files\GameNoLag` 只有 Administrators 可写。`ProgramData\GameNoLag` 只有 SYSTEM 和 Administrators 可读 |
| 静态存储的凭据 | 贡献者密钥和中继令牌以 SHA-256 哈希存储 |
| 传输中的凭据 | 安装程序拒绝非 HTTPS 的控制平面 URL |
| 中继被当作开放代理 | FORWARD 策略为 DROP。出站只允许发往已发布的游戏地址段（ipset）。每个会话每个方向 64 KB/s |
| 一个客户端收到另一个客户端的流量 | 数据库中的 `UNIQUE (relay_id, inner_ip)` |
| 新中继吸引流量 | 在通过外部验证前保持 `pending`，且在 `trusted_after` 之前不会被提供 |
| 单人污染配置 | 进入配置需要三个不同的密钥，并与公布的地址段匹配 |
| 反作弊 | 仅通过进程名检测游戏；不打开、读取或挂钩（hook）任何游戏进程 |

## 保存的数据

| 位置 | 内容 |
|---|---|
| 控制平面 | 密钥哈希、中继记录和计数器、设备公钥和指纹（主机名）、对端绑定、已发布的配置、按游戏和密钥哈希记录的观测目标 `ip:port` |
| 中继 | 其 WireGuard 私钥（`/etc/gnl/relay.key`，0600）、其令牌、WireGuard 对端 |
| 客户端 | 设备私钥、配置、滚动的 8 MB 日志 |

任何地方都不会记录玩家游戏流量的载荷（payload）或来源地址。
