# GameNoLag 文档

[English](../en/README.md) · [Tiếng Việt](../vi/README.md) · **简体中文**

## 从这里开始

| 如果你是… | 请阅读 |
|---|---|
| 玩家 | [用户指南](user-guide.md) |
| 正在安装或打包 Windows 客户端 | [客户端部署](setup-client.md) |
| 将 VPS 贡献为中继（relay） | [中继部署](setup-relay.md) |
| 为他人运行该服务 | [控制平面部署](setup-control-plane.md) |
| 想了解整体如何协作 | [架构](architecture.md) |
| 贡献代码 | [CONTRIBUTING](../../CONTRIBUTING.md)（英文） |
| 报告安全漏洞 | [SECURITY](../../SECURITY.md)（英文） |

## 按顺序搭建一套完整部署

1. **控制平面（control plane）。** Postgres，置于 TLS 之后的 `gnl-control`，然后签发一个
   贡献者密钥（key）。
   → [setup-control-plane.md](setup-control-plane.md)
2. **第一个中继。** 一台靠近游戏服务器的 KVM VPS：运行 `relay-v1.sh`，在服务商处开放
   UDP 51820，并用 `gnl-relaycheck` 从外部进行验证。
   → [setup-relay.md](setup-relay.md)
3. **游戏配置（profile）。** 先用 `gnl-profile` 试运行，审阅输出，然后加上
   `-publish`。
   → [setup-control-plane.md § 7](setup-control-plane.md#7-publish-a-game-profile)
4. **客户端。** 使用贡献者密钥安装 Windows 安装包，然后点击 Connect。
   → [setup-client.md](setup-client.md)

中继状态变为 `up`，且其 48 小时观察期结束后，才会提供给玩家使用。

## 深入参考（英文）

这些文档解释了每一步背后的理由，以及每项检查所证明的内容。

- [Windows 客户端运行手册](../windows-client-runbook.md)
- [P1 运行手册：控制平面与第一个中继](../p1-runbook.md)
- [P0 运行手册：线路质量测量](../p0-runbook.md)
