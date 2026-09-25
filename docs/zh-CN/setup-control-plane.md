# 部署控制平面

[English](../en/setup-control-plane.md) · [Tiếng Việt](../vi/setup-control-plane.md) · **简体中文**

控制平面（control plane，即 `gnl-control`）保存贡献者密钥（key）、设备名额（device slot）、
中继（relay）记录和已发布的游戏配置（profile）。它**不在**数据路径上。玩家的数据包从不经过它，
因此它可以运行在任何拥有公网 HTTPS 地址的地方，它与玩家之间的延迟也无关紧要。

你需要：

- 一台 Linux 主机，任何使用 systemd 的发行版均可。
- PostgreSQL 13 或更高版本。
- 一个域名和一张 TLS 证书。`gnl-control` 只提供明文 HTTP，需要在其前面放置反向代理。
- `gnl-control` 和 `gnl-profile` 二进制文件：来自 CI 构件 `gamenolag-linux-amd64`，
  或从源码构建（见下文）。

## 1. 构建或获取二进制文件

```bash
git clone https://github.com/hashcott/NoLag.git && cd NoLag
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-control ./cmd/gnl-control
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-profile ./cmd/gnl-profile
sudo install -m 0755 gnl-control gnl-profile /usr/local/bin/
```

## 2. 创建数据库

```bash
sudo -u postgres createuser --pwprompt gamenolag
sudo -u postgres createdb --owner gamenolag gamenolag
```

每次 `gnl-control` 启动时都会应用数据库结构（schema）。每条语句都是幂等的，
因此不需要单独的迁移步骤。

## 3. 用 systemd 运行

`/etc/gnl/control.env`，权限 `0600`，属主为 root：

```bash
GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
```

`/etc/systemd/system/gnl-control.service`：

```ini
[Unit]
Description=GameNoLag control plane
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
EnvironmentFile=/etc/gnl/control.env
ExecStart=/usr/local/bin/gnl-control -listen 127.0.0.1:8080 -trust-proxy
DynamicUser=yes
Restart=on-failure
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gnl-control
curl -s http://127.0.0.1:8080/healthz
```

### 参数

| 参数 | 默认值 | 含义 |
|---|---|---|
| `-dsn` | `$GNL_DSN` | Postgres 连接字符串。必填 |
| `-listen` | `:8080` | HTTP 监听地址。前面有代理时应绑定到回环地址 |
| `-trust-proxy` | 关闭 | 使用 `X-Forwarded-For` 进行限速（rate limit）。**只有在你的代理确实会设置该请求头时才开启。** 否则每个客户端都能自选限速桶 |
| `-stale-after` | `5m` | 中继超过这么长时间未同步，就标记为 `down` |
| `-poll-secs` | `10` | 通知中继同步的间隔 |
| `-mint-key` | — | 签发一个贡献者密钥，打印后退出 |

## 4. 在前面加上 TLS

中继令牌（token）和贡献者密钥以 bearer 令牌的形式传输。若使用明文 HTTP，
路径上的任何人都能复制它们。两个安装程序都会拒绝非 `https` 的控制平面 URL。

一份最简 Caddy 配置即可自行获取并续期证书：

```
cp.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

使用 nginx 时，要传递客户端地址，以便 `-trust-proxy` 统计的是真实调用方：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

从外部检查：`curl -s https://cp.example.com/healthz`。

## 5. 签发贡献者密钥

```bash
sudo sh -c 'set -a; . /etc/gnl/control.env; exec /usr/local/bin/gnl-control -mint-key'
```

密钥只打印**一次**。系统只保存其哈希，因此无法找回。请通过你信任的渠道交给贡献者。
默认情况下，一个密钥最多激活三台设备；对应数据库中的 `contributor_key.max_devices`。

## 6. 从外部验证每个中继

新中继会保持 `pending` 状态，在从中继外部发起的检查证明其 UDP 端口已开放之前，
永远不会提供给玩家。参见
[中继部署 → 从外部验证](setup-relay.md#5-verify-from-outside)。

中继状态：

| 状态 | 含义 | 是否提供给玩家 |
|---|---|---|
| `pending` | 已注册，从未通过外部验证 | 否 |
| `up` | 已验证可达，且仍在同步 | 是，在 `trusted_after`（注册后 48 小时）过后 |
| `unreachable` | 外部检查失败；原因已记录 | 否 |
| `down` | 超过 `-stale-after` 未同步 | 否 |

```sql
SELECT id, region, status, active_peers, capacity, last_seen, trusted_after,
       unreachable_detail
  FROM relay ORDER BY status, id;
```

<a id="7-publish-a-game-profile"></a>
## 7. 发布游戏配置

在配置存在之前，每个中继的出站白名单（egress allowlist）都是空的，中继不转发任何流量。
这是预期的初始状态。

配置根据贡献者的观测构建，并与云服务商公布的地址段交叉核验。请先不带 `-publish` 运行：
它会打印将要发布的内容，但不做任何修改。

```bash
export GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1
# review the list, then:
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1 -publish
```

| 参数 | 含义 |
|---|---|
| `-game` | 游戏 id，与观测中上报的一致。必填 |
| `-aws-regions` | 计入其公布地址段的 AWS 区域 |
| `-azure-regions`、`-azure-url` | Azure 区域，以及当前 Service Tags JSON 的 URL |
| `-asns` | 计入其宣告前缀的 ASN |
| `-min-reporters` | 一个地址所需的独立密钥数（默认 3） |
| `-publish` | 真正发布。不加此参数则不写入任何内容 |

与任何公布地址段都不匹配的地址会被列为 unverified（未验证），并且永远不会被加入。
它们通常是语音聊天或 CDN；手动加入它们会把同一地址块中的其他所有流量也一并路由过去。

中继会在一次同步内获取新配置。客户端在下次连接时，或点击 *Refresh game list*（刷新游戏列表）时获取。

## 日常运维

| 任务 | 方法 |
|---|---|
| 查看中继集群 | 第 6 步中的 `SELECT` |
| 释放设备名额 | 贡献者携带其密钥调用 `DELETE /v1/devices/{id}`，或执行 `DELETE FROM device WHERE id = '…';` |
| 吊销密钥 | `UPDATE contributor_key SET status = 'revoked' WHERE key_hash = '…';` 哈希值为 `printf %s 'GNL-…' \| sha256sum`。一次同步之内，该密钥的设备会从所有中继上移除，其名下的中继不再分配给玩家、也不再承载任何流量，其观测记录也不再计入配置。该密钥将无法再注册、激活、上报可达性或提交观测 |
| 移除中继 | `DELETE FROM relay WHERE id = '…';` 其对端绑定会一并删除 |
| 日志 | `journalctl -u gnl-control -f` |
| 备份 | `pg_dump gamenolag`。数据库就是全部状态；二进制文件是无状态的 |

## 自行构建二进制文件

需要 Go 1.25 或更高版本：

```bash
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

关于更深入的运维流程——第一个中继、手动绑定对端、每项检查证明了什么——请参见
[P1 运行手册](../p1-runbook.md)（英文）。
