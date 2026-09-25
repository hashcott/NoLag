# GameNoLag

**借助部署在游戏服务器旁、由社区运营的中继，让越南玩家的延迟更稳定。**

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) · [Tiếng Việt](README.vi.md) · **简体中文**

越南玩家通常被匹配到新加坡的游戏服务器，匹配系统溢出时则会被分到东京。到了晚上，
运营商（ISP）通往这些地区的线路经常拥堵，对局中途延迟飙升、丢包频发。而一台位于新加坡、
属于其他服务商、走不同国际转接线路（transit）的 VPS，往往能以更干净的路径到达同样的服务器。

GameNoLag 让你的游戏走上这条路径。它**只把游戏流量**通过一台靠近游戏服务器的
WireGuard 中继（relay）转发。中继是通过测量你自己的网络连接来选出的；一旦出现问题，
GameNoLag 会立即让路。所有中继都是社区贡献的 VPS。

> **状态：早期阶段，尚未交到玩家手中。** 所有组件均已在 CI 中构建并测试，覆盖 Linux 与
> Windows，并使用真实内核与数据库。Windows 客户端尚未经过玩家实际运行，在此之前需要先完成
> [Windows 手动检查清单](docs/windows-client-runbook.md#the-window)（英文）。

## 有何不同

- **只走游戏流量。** 这不是 VPN。某个游戏运行时才为其服务器安装路由，游戏退出即移除。
  浏览器、Discord 和下载永远不会离开你的普通连接。
- **由你的线路决定。** 每个候选中继都会从你的电脑发起一次真实的 WireGuard 握手来测量，
  最快的胜出。对局中途不会为了一点点提升而切换中继。
- **从不触碰游戏。** 游戏只通过进程名识别，与任务管理器列出进程的方式相同。
  不会打开、读取或注入任何游戏进程。
- **故障时安全回退。** 中继失效时，客户端会切换到另一台；没有中继响应时，客户端会移除路由，
  游戏继续经由运营商线路运行。停止服务或重启电脑后不会留下任何痕迹。
- **中继无法被滥用。** 中继只转发到已发布的游戏地址段，并对每位玩家限速。中继在投入使用前
  需要从外部验证；吊销某位贡献者的密钥后，一次同步之内即将其移出网络。

## 工作原理

```
  Your PC (Windows)                 Relay (community VPS)           Game server
 ┌────────────────────┐  WireGuard  ┌──────────────────────┐        ┌───────────┐
 │ game traffic only  │ ══════════► │ game ranges only,    │ ─────► │ Singapore │
 │ fastest relay wins │             │ rate-capped          │        │ Tokyo     │
 └─────────┬──────────┘             └──────────┬───────────┘        └───────────┘
           │ HTTPS                             │ sync every 10 s
           ▼                                   ▼
       ┌──────────────────────────────────────────────────┐
       │ Control plane: keys, devices, relays, game ranges │
       └──────────────────────────────────────────────────┘
```

控制平面（control plane）决定谁可以使用哪台中继，并发布游戏的地址段。它从不位于数据包的
传输路径上。游戏地址段保持精确：一个地址只有在被三位相互独立的贡献者观测到，且位于 AWS、
Azure 或游戏自身网络公布的地址段之内时，才会被加入。

详见：[架构](docs/zh-CN/architecture.md)。

## 快速开始

| 我想要… | 从这里开始 |
|---|---|
| **玩游戏**，获得更低、更稳定的延迟 | [用户指南](docs/zh-CN/user-guide.md) |
| **贡献一台 VPS** 作为中继 | [部署中继](docs/zh-CN/setup-relay.md) |
| 为社区**运营一套部署** | [部署控制平面](docs/zh-CN/setup-control-plane.md)，然后按照[部署顺序](docs/zh-CN/README.md)进行 |
| **安装或打包** Windows 客户端 | [部署客户端](docs/zh-CN/setup-client.md) |
| **参与开发** | [开发](#开发)与 [CONTRIBUTING](CONTRIBUTING.md)（英文） |

玩家需要从部署运营者处获得一个贡献者密钥（contributor key）。现阶段，密钥发放给贡献中继的人，
每个密钥最多可用于其本人的三台电脑。

## 包含哪些组件

| 程序 | 运行于 | 作用 |
|---|---|---|
| `gnl-service` | Windows，作为服务 | 维持隧道、测量中继、为正在运行的游戏设置路由 |
| `gnl-ui` | Windows，以用户身份 | 托盘图标、迷你面板和完整窗口。无特权 |
| `gnl-control` | Linux | 控制平面：密钥、设备名额、中继、游戏配置 |
| `gnl-agent` | 每台中继 | 同步 WireGuard 对端、出站白名单和防火墙 |
| `gnl-relaycheck` | 中继之外 | 证明中继可从互联网访问 |
| `gnl-profile` | 运维者 | 根据观测记录与已公布地址段生成某个游戏的地址列表 |
| `gnl-probe`、`gnl-analyze` | 测量主机 | 测量高峰时段经由 VPS 的线路是否优于运营商 |

## 开发

需要 Go 1.25 或更高版本。内核与数据库测试需要 Docker。

```bash
go test ./...                               # runs anywhere
GOOS=windows go vet ./...                   # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh       # relay firewall against a real kernel
GNL_TEST_DSN=postgres://… go test ./internal/control/   # control-plane store against Postgres
```

仅限 Windows 的代码位于构建标签之后，其决策逻辑放在与平台无关的文件中，因此几乎所有内容都能在
任何机器上测试。CI 会在每次推送和每个拉取请求时运行以上全部命令，并构建可下载的 Linux 程序与
Windows 安装包。

构建命令、代码约定以及修改时不得放宽的边界，见 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。

## 安全

客户端只有一道特权边界：托盘程序通过命名管道与服务通信，该管道只接受四条无参数命令，
且只接受当前登录用户的连接。密钥与令牌仅以哈希形式存储。中继会丢弃所有不是发往游戏地址段的流量。

完整列表见[架构](docs/zh-CN/architecture.md)。发现漏洞请按 [SECURITY.md](SECURITY.md)（英文）私下报告。

## 文档

提供 [English](docs/en/README.md)、[Tiếng Việt](docs/vi/README.md) 和
[简体中文](docs/zh-CN/README.md) 三种语言的指南：用户指南、客户端/中继/控制平面部署、架构。

深入的运行手册（英文）：
- [Windows 客户端](docs/windows-client-runbook.md)
- [控制平面与第一台中继](docs/p1-runbook.md)
- [线路质量测量](docs/p0-runbook.md)

## 参与贡献

欢迎提交 issue 和拉取请求。请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)（英文）。所有参与者
均须遵守[行为准则](CODE_OF_CONDUCT.md)（英文）。

## 许可证

[Apache License 2.0](LICENSE)。
