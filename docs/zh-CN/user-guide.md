# GameNoLag 用户指南

[English](../en/user-guide.md) · [Tiếng Việt](../vi/user-guide.md) · **简体中文**

GameNoLag 让你的游戏流量经由靠近游戏服务器（新加坡、东京）的中继（relay）传输，
而不是走运营商（ISP）的默认路由。只有游戏流量经过中继；浏览器、Discord、下载以及其他一切
仍走你的普通网络连接。

本指南面向玩家。关于安装和打包，请参见[部署 Windows 客户端](setup-client.md)。

## 你需要准备

- Windows 10 或 11，64 位。
- 安装时需要管理员权限。此后日常使用不需要。
- 由运维者提供的**贡献者密钥**（contributor key，`GNL-XXXX-XXXX-XXXX-XXXX`），在你贡献一台 VPS 时发放。
  一个密钥最多可激活 **3 台电脑**。
- **控制平面地址**（control plane，以 `https://` 开头），同样由运维者提供。

## 安装

1. 从[最新版本](https://github.com/hashcott/NoLag/releases/latest)下载 **`GameNoLag-Setup-<version>.exe`**。
2. 双击运行。Windows 会请求管理员权限，请允许。在安装程序完成代码签名之前，
   Windows SmartScreen 可能会提示 *Windows protected your PC*（Windows 已保护你的电脑）。
   点击 **More info → Run anyway**（更多信息 → 仍要运行），但仅限于从上面的版本页面下载的文件。
3. 接受许可协议，然后输入你的**贡献者密钥**和**控制平面地址**。地址可能已经预先填好。
4. 保持勾选 **Start GameNoLag**，完成安装。图标会出现在通知区域，此后每次登录时它都会自动启动。

升级方法相同：在旧版本上运行更新的安装程序。你的电脑会保留其身份，因此不会额外占用设备名额。

## 日常使用

### 托盘图标

GameNoLag 位于任务栏右侧的通知区域。如果看不到它，请点击 `^` 箭头。

| 图标 | 含义 |
|---|---|
| 灰色空心圆环 | 未连接，或服务未运行 |
| 绿色圆环，中心实心 | 已连接。游戏运行时，其流量经由中继传输 |
| 红色圆环，带一道横杠 | 已连接，但出现问题，例如中继丢失或路由安装失败 |

三种状态不仅颜色不同，形状也不同。将鼠标悬停在图标上，可以查看中继和延迟。

**右键单击**打开菜单：

| 菜单项 | 作用 |
|---|---|
| （第一行） | 当前状态。只读 |
| Open window | 打开完整窗口 |
| Connect | 测量每个中继并选出最快的一个。需要几秒到几十秒 |
| Disconnect | 让所有流量回到你的普通网络路径 |
| Refresh game list | 重新加载游戏地址段 |
| Open log folder | 打开日志文件夹。出现 *Access denied* 属于正常现象；见“故障排查” |
| Exit | 关闭图标。**不会断开连接**；如需断开，请先点击 Disconnect |

### 迷你面板和完整窗口

**左键单击**图标，会在任务栏上方打开迷你面板。它显示延迟（rtt）及一个小柱状图、丢包率、
中继、游戏、一个 Connect/Disconnect 按钮，以及未连接时的原因。

| 控件 | 作用 |
|---|---|
| **+** | 打开完整窗口：3 分钟延迟图表、当前生效的路由，以及事件日志（连接、中继切换、游戏启动和退出、故障） |
| **−** | 在完整窗口中，返回迷你面板 |
| **×** | 隐藏迷你面板。再次左键单击图标效果相同 |
| **Tab** / **Enter** | 在按钮之间切换 / 按下当前选中的按钮 |

关闭或隐藏窗口**永远不会断开连接**。图表和日志只保存在内存中，每次 GameNoLag 启动时都从空白开始。
图表中的空白段表示那一刻没有测量数据（未连接，或服务无响应），而不是延迟为零。

迷你面板始终显示在其他窗口之上。在**无边框**或**窗口化**模式下，它会显示在游戏上方，
但在**独占全屏**模式下不会。如果不想让它遮挡游戏，请在开始游戏前按 **×**。

### 游戏过程中

你什么都不需要做。连接后，GameNoLag 会察觉列表中的游戏启动，并只路由该游戏。
游戏退出后，路由即被移除。

| 游戏 | 进程 |
|---|---|
| PUBG | `TslGame.exe` |
| Valorant | `VALORANT-Win64-Shipping.exe` |
| League of Legends | `League of Legends.exe` |
| Counter-Strike 2 | `cs2.exe` |
| Dota 2 | `dota2.exe` |

GameNoLag **仅通过进程名**识别游戏，与任务管理器中显示的名称一致。它从不打开、读取或修改游戏进程；
它在底层的网络协议栈中工作。

它不会仅仅因为另一个中继变快了就在对局中途切换中继，因为那会造成卡顿。它会在两局之间切换。
例外情况是中继彻底失效（150 秒无响应）：此时 GameNoLag 会立即切换到另一个中继。
如果都无响应，你的游戏会回到普通网络路径，GameNoLag 每 30 秒重试一次。

## 故障排查

| 现象 | 可能原因 | 处理方法 |
|---|---|---|
| *The GameNoLag service is not running* | 服务已停止 | 管理员 PowerShell：`Start-Service GameNoLag` |
| 点击 Connect 后出现 *no relay answered* | 你的网络阻断了 UDP（在办公室或咖啡馆网络中很常见） | 换一个网络试试。你的游戏仍可在普通网络路径上正常运行 |
| *no device slots left* | 该密钥已绑定 3 台电脑 | 在不再使用的电脑上移除其身份（见“卸载”），或联系运维者 |
| 红色图标带横杠 | 已连接但出现故障 | 在完整窗口中查看错误信息。通常会自动恢复；若没有恢复，先 Disconnect 再 Connect |
| *Open log folder*：Access denied | 该文件夹存放着你的私钥，因此只有管理员可以读取 | 属于正常现象。日志位于 `C:\ProgramData\GameNoLag\service.log`；请以管理员身份运行记事本来打开它 |
| 完全无法上网，且你怀疑与 GameNoLag 有关 | — | `Stop-Service GameNoLag`，或重启。服务一旦停止，GameNoLag 的所有路由都会消失 |

寻求帮助时，请发送 `service.log`，并说明问题发生时你正在做什么。

## 卸载

**Settings → Apps → Installed apps → GameNoLag → Uninstall**（设置 → 应用 → 已安装的应用 → GameNoLag → 卸载）。
这会移除程序，并保留这台电脑的身份，因此之后重新安装不会额外占用设备名额。

如果还想释放名额（例如你要把这台电脑送人），请同时移除身份。在管理员 PowerShell 中：

```powershell
Remove-Item -Recurse -Force "$env:ProgramData\GameNoLag"
```

之后的下一次安装会被视为一台新电脑。运维者也可以帮你释放名额。

## 隐私

- 只有列表中游戏的流量会经过中继。其他一切仍走你的普通网络路径。
- GameNoLag 不会读取你的流量内容，也不会读取游戏进程内部的任何内容。
- 你电脑的私钥和贡献者密钥存放在 `C:\ProgramData\GameNoLag`，只有 SYSTEM 和管理员可以读取。
  控制平面只保存你的贡献者密钥的哈希值。
