# GameNoLag

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) · [Tiếng Việt](README.vi.md) · **简体中文**

当经由中继的线路优于运营商（ISP）默认路由时，GameNoLag 会把越南玩家的游戏流量
通过部署在游戏服务器附近的 WireGuard 中继（relay）转发出去。只有游戏流量经过中继，
机器上的其他流量仍走原本的网络路径。

中继是社区贡献的 VPS。贡献者会获得一个密钥（key），该密钥最多可激活其本人的三台机器，
这些机器的流量将经由中继集群路由。

> **状态：早期阶段。** 下文列出的每个组件都已实现并经过测试。Windows
> 客户端已在 Windows 上通过 CI，但尚未由真实玩家运行过；在此之前，
> [客户端运行手册](docs/windows-client-runbook.md#the-window)（英文）中的手动检查清单是必须通过的关卡。

## 工作原理

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

- **客户端负责测量，控制平面（control plane）负责筛选。** 控制平面下发处于在线且可信状态的中继。
  客户端通过玩家自己的网络路径与每个中继进行握手（handshake），并选出最快的一个。
  控制平面无法对中继排序，因为它看不到任何玩家的网络路径。
- **游戏地址段范围窄，且经过交叉核验。** 一个地址只有在被三名相互独立的贡献者观测到，
  并且落在 AWS、Azure 或该游戏所属 ASN 公布的地址段内时，才会进入已发布的配置（profile）。
  路由在游戏运行期间安装，游戏退出后即被移除。
- **出现故障时回退到普通路径。** 如果中继不再响应，客户端会切换到另一个中继；
  如果所有中继都无响应，它会移除自己的路由。服务停止时，隧道网卡和所有路由随之消失。
  重启即可让机器完全恢复原状。

## 组件

| 命令 | 运行位置 | 作用 |
|---|---|---|
| `gnl-service` | Windows，LocalSystem | 持有隧道。获取会话和配置，测量中继，安装和移除路由。 |
| `gnl-ui` | Windows，以当前用户身份 | 系统托盘图标、迷你面板和完整窗口。只向服务发送 `connect`、`disconnect`、`status`、`reload-profile` 之一，别无其他。 |
| `gnl-control` | Linux | 控制平面：贡献者密钥、设备名额（device slot）、中继注册与同步、游戏配置。也负责签发密钥（`-mint-key`）。 |
| `gnl-agent` | Linux 中继 | 将 WireGuard 对端（peer）、出站白名单（egress allowlist，基于 ipset）和防火墙与控制平面保持一致。 |
| `gnl-relaycheck` | 中继之外的任意位置 | 证明中继的 UDP 端口可从互联网访问，并将结果报告给控制平面。 |
| `gnl-profile` | 运维者 | 根据观测到的地址和公开发布的地址段，构建某款游戏的 CIDR 列表。除非指定 `-publish`，否则只做试运行（dry run）。 |
| `gnl-probe`、`gnl-analyze` | 测量主机 | P0 线路质量测量：高峰时段 VPS 线路是否优于 ISP 线路？ |

## 仓库结构

```
cmd/            one directory per binary above
internal/
  api/          request and response types shared by client, relay and control plane
  control/      HTTP server, Postgres store, schema, rate limits
  agent/        relay firewall rules and the sync loop
  ipsetsync/    atomic ipset swaps for the egress allowlist
  wgsync/       WireGuard peer reconciliation
  profile/      turning observations and published ranges into a narrow profile
  probe/, analyze/, stats/   the P0 measurement campaign
  client/       the Windows client: ipc, routes, wintun, winpipe, gamewatch, pick, …
deploy/         relay installer, Windows install/uninstall, verification scripts
docs/           runbooks and guides (see below)
```

## 开发

需要 Go 1.25 或更高版本。大部分代码可在任何操作系统上构建和测试。
仅限 Windows 的部分通过构建标签（build tag）隔离，其决策逻辑放在与平台无关的文件中，
因此在所有平台上都能测试。

```bash
go test ./...                       # everything that runs anywhere
GOOS=windows go vet ./...           # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh   # iptables/ipset against a real kernel (needs Docker)
```

防火墙测试会修改本机防火墙。它们受 `linuxroot` 构建标签和 `GNL_FIREWALL_TESTS=1` 控制，
因此 `go test ./...` 永远不会触碰 iptables。该脚本在一个特权的一次性容器中运行这些测试。

### 构建

```bash
# Windows client
GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o gnl-ui.exe ./cmd/gnl-ui

# relay and control plane
GOOS=linux GOARCH=amd64 go build -o gnl-agent ./cmd/gnl-agent
GOOS=linux GOARCH=amd64 go build -o gnl-control ./cmd/gnl-control
```

`gnl-ui.exe` 发布时必须在同一目录下附带 `deploy/windows/gnl-ui.exe.manifest`。
缺少该清单文件（manifest）时，托盘菜单不会被创建。

### CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml)（英文）在每次推送到 `main`
以及每个拉取请求（pull request）时运行：

1. 在 Ubuntu 和 Windows 上运行 `gofmt`、`go vet` 和 `go test`。在 Ubuntu 上还会对
   Windows 构建执行 vet 检查。
2. 在真实内核上运行防火墙测试。
3. 两者都通过后，构建 Linux 二进制文件和 Windows 客户端包（两个可执行文件、清单文件和安装脚本）。
   两者都可从该次运行的构件（artifacts）中下载。

### 约定

- 提交信息遵循 Conventional Commits（`feat(client): …`、`fix(relay): …`），
  正文需说明原因。
- 做出决策的逻辑必须有测试。只调用操作系统的代码保持精简，使未被测试的部分尽可能少。
- 注释解释“为什么”，而不是“做了什么”。

## 安全模型概要

- **客户端只有一道权限边界。** UI 以非特权身份运行，只能向服务请求四个不带参数的动词（verb）。
  它无法指定中继、路由、文件或命令。命名管道只允许当前交互式登录的用户连接。
- **机密信息以哈希形式存储。** 贡献者密钥和中继令牌（token）只以哈希形式存储。
  设备私钥存放在 `C:\ProgramData\GameNoLag`，只有 SYSTEM 和 Administrators 可以读取。
- **中继不是开放代理。** FORWARD 策略为 DROP。出站流量仅限于已发布的游戏地址段，
  且每个会话在每个方向上限速（rate limit）为 64 KB/s。新中继在其可达性通过外部验证、
  且观察期结束之前，不承载任何流量。
- **从不触碰游戏本身。** 游戏通过进程名识别，方式与任务管理器相同。
  客户端从不打开游戏进程的句柄、读取其内存或注入任何内容。
- **配置需要证据。** 观测记录只包含目标地址，并且单个贡献者无法独自让某个地址进入配置。

## 文档

完整文档提供 [English](docs/en/README.md)、
[Tiếng Việt](docs/vi/README.md) 和 [简体中文](docs/zh-CN/README.md) 版本。

| 指南 | 适用对象 |
|---|---|
| [用户指南](docs/zh-CN/user-guide.md) | 玩家：安装、使用、故障排查、卸载 |
| [客户端部署](docs/zh-CN/setup-client.md) | 安装、配置和打包 Windows 客户端 |
| [中继部署](docs/zh-CN/setup-relay.md) | 在 VPS 上运行中继的贡献者 |
| [控制平面部署](docs/zh-CN/setup-control-plane.md) | 运维者：Postgres、TLS、密钥、验证中继、游戏配置 |
| [架构](docs/zh-CN/architecture.md) | 各部分如何协作、API、中继生命周期、安全边界 |

深入参考：[Windows 客户端运行手册](docs/windows-client-runbook.md)（英文）、
[P1 运行手册](docs/p1-runbook.md)（英文）和 [P0 运行手册](docs/p0-runbook.md)（英文）。

## 参与贡献

欢迎参与贡献。请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）；发现安全漏洞时，
请按 [SECURITY.md](SECURITY.md)（英文）所述私下报告，不要提交公开 issue。
所有参与者都须遵守[行为准则](CODE_OF_CONDUCT.md)（英文）。

## 许可证

[Apache License 2.0](LICENSE)（英文）。
