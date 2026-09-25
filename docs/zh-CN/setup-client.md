# 部署 Windows 客户端

[English](../en/setup-client.md) · [Tiếng Việt](../vi/setup-client.md) · **简体中文**

客户端由两个进程组成：

- **`gnl-service`** 以 LocalSystem 身份运行，负责所有需要特权的工作：Wintun 网卡、WireGuard、
  路由、游戏检测。
- **`gnl-ui`** 以当前登录用户身份运行，不持有任何特权：系统托盘图标、迷你面板和完整窗口。

UI 通过命名管道向服务请求四个动词（verb）之一，别无其他。

本页面向负责安装和打包客户端的人员。日常使用请参见[用户指南](user-guide.md)。

## 要求

- Windows 10 或 11，64 位。
- 安装需要管理员权限。日常使用不需要。
- 由运维者提供的贡献者密钥（key）和控制平面（control plane）URL。

## 1. 获取安装包

CI 构件 `gamenolag-windows-amd64` 包含所需的全部文件，放在同一目录下：

| 文件 | 说明 |
|---|---|
| `gnl-service.exe` | 服务 |
| `gnl-ui.exe` | 托盘和窗口，使用 `-H=windowsgui` 构建 |
| `gnl-ui.exe.manifest` | **必须与 `gnl-ui.exe` 放在一起。** 缺少它时，托盘菜单不会被创建，且图标在高 DPI 屏幕上会发虚 |
| `install.ps1`、`uninstall.ps1` | 安装程序和卸载程序 |

在任何操作系统上自行构建：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags -H=windowsgui -o dist/gnl-ui.exe ./cmd/gnl-ui
cp deploy/windows/gnl-ui.exe.manifest deploy/windows/*.ps1 dist/
```

## 2. 安装

在**管理员** PowerShell 中，进入存放安装包的文件夹：

```powershell
Unblock-File .\*.ps1, .\*.exe
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://cp.example.com
```

`Unblock-File` 会去除“从互联网下载”的标记。没有这一步，PowerShell 会拒绝运行未签名的下载脚本。
安装程序会拒绝非 `https` 的控制平面 URL，因为密钥以 bearer 令牌（token）的形式传输。

它会做以下事情：

| 位置 | 内容 |
|---|---|
| `C:\Program Files\GameNoLag\` | 二进制文件和清单文件。用户可读，只有 Administrators 可写，因为一个可被用户替换的 LocalSystem 二进制文件，会让该用户获得 LocalSystem 权限 |
| `C:\ProgramData\GameNoLag\` | 状态。只有 SYSTEM 和 Administrators 可读，因为其中存放着设备私钥和贡献者密钥 |
| 服务 `GameNoLag` | 注册为自动启动。在 UI 请求连接之前，隧道不会启动 |
| 开始菜单、登录 | 一个 *GameNoLag* 快捷方式，并设置托盘在每次登录时启动 |

安装程序**不会**启动托盘。它以提升的权限运行，因此它启动的任何程序也会以提升的权限运行，
并出现在管理员的会话中。请从开始菜单启动 *GameNoLag*，或注销后重新登录。

## 3. 配置

`C:\ProgramData\GameNoLag\config.json` 由安装程序写入：

```json
{
  "control_url": "https://cp.example.com",
  "contributor_key": "GNL-XXXX-XXXX-XXXX",
  "games": [
    { "id": "pubg", "process_names": ["TslGame.exe"] }
  ]
}
```

- `games` 为可选项。设置后会**替换**内置列表。
- 未知字段会被拒绝，因此拼写错误会明确报错，而不是悄无声息地什么都不做。
- 编辑后需重启服务：`Restart-Service GameNoLag`。

内置游戏：

| id | 进程 |
|---|---|
| `pubg` | `TslGame.exe` |
| `valorant` | `VALORANT-Win64-Shipping.exe` |
| `lol` | `League of Legends.exe` |
| `csgo` | `cs2.exe` |
| `dota2` | `dota2.exe` |

同一文件夹中的其他文件：

| 文件 | 说明 |
|---|---|
| `device.key` | 本机的 WireGuard 私钥。只生成一次并长期保留，因为每个新身份都会占用一个设备名额（device slot）。损坏的文件会被拒绝，而不会被替换；如确有必要，请有意识地手动删除 |
| `service.log` | 超过 8 MB 时滚动为 `service.log.1` |

## 4. 首次连接

1. 打开托盘菜单，选择 **Connect**（连接）。
2. 首次使用时，设备会以主机名作为名称自动激活。这会占用该密钥的一个设备名额（默认共三个）。
3. 客户端与每个中继（relay）握手（handshake），选出最快的一个并固定使用。
4. 列表中的游戏启动时，安装其路由；游戏退出时，移除这些路由。

如果激活因 *slots full*（名额已满）而失败，错误信息会列出占用名额的机器。
可以在不再使用的机器上用 `-Purge` 卸载来释放一个名额，或联系运维者。

## 5. 日志

`C:\ProgramData\GameNoLag\service.log` 需要管理员权限才能读取。

| 日志行 | 含义 |
|---|---|
| `no relay answered` | 网络阻断了发往中继的 UDP。路由已移除，玩家走其普通网络路径 |
| `relay X has not handshaken in over 2m30s` | 该中继已停止响应。客户端正在重新测量 |
| `relay X: ... has no IPv4 address` | 已跳过该中继。客户端只路由 IPv4 |
| `profile version N: ignoring "..."` | 配置（profile）中一个格式错误的前缀被丢弃 |
| `... is not a usable key file` | `device.key` 已损坏。删除它会消耗一个设备名额 |

在控制台中实时查看服务输出（仍需以 LocalSystem 身份运行）：

```
psexec -s -i "C:\Program Files\GameNoLag\gnl-service.exe"
```

## 6. 卸载

```powershell
.\uninstall.ps1          # keeps this machine's identity and its device slot
.\uninstall.ps1 -Purge   # also deletes it; reinstalling costs a new slot
```

停止服务即可撤销它对网络的影响。网卡随进程一起消失，所有指向它的路由也随之消失。

## 如果玩家的网络出现故障

停止服务（`Stop-Service GameNoLag`）或重启。两者都能让机器完全恢复：

- 网卡由服务创建，而非复用已有网卡，并在服务退出时消失。
- 路由只写入活动存储，从不持久化，因此重启即可清除。

关于客户端的内部工作原理，请参见
[Windows 客户端运行手册](../windows-client-runbook.md)（英文）。
