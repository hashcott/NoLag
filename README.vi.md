# GameNoLag

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) · **Tiếng Việt** · [简体中文](README.zh-CN.md)

GameNoLag đưa lưu lượng game của người chơi ở Việt Nam đi qua một relay
WireGuard đặt gần máy chủ game, vào những giờ mà đường này tốt hơn route mặc
định của nhà mạng. Chỉ game đi qua relay. Mọi thứ khác trên máy vẫn đi đường
Internet bình thường.

Các relay là VPS do cộng đồng đóng góp. Người đóng góp (contributor) nhận một
key. Key đó kích hoạt được tối đa ba máy của chính họ, và các máy này được
route qua đội relay.

> **Trạng thái: giai đoạn đầu.** Mọi thành phần dưới đây đều đã có và đã được
> kiểm thử. Client Windows đã qua CI trên Windows nhưng chưa được người chơi nào
> chạy thật; checklist thủ công trong
> [runbook của client](docs/windows-client-runbook.md#the-window) (tiếng Anh)
> là điều kiện phải qua trước khi điều đó xảy ra.

## Cách hoạt động

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

- **Client đo, control plane lọc.** Control plane đưa ra danh sách các relay
  đang up và đáng tin. Client handshake với từng relay qua chính đường mạng
  của người chơi rồi chọn relay nhanh nhất. Control plane không thể xếp hạng
  relay, vì nó không nhìn thấy đường mạng của bất kỳ người chơi nào.
- **Dải địa chỉ game hẹp và được đối chiếu chéo.** Một địa chỉ chỉ vào được
  profile đã công bố sau khi ba contributor độc lập cùng thấy nó, và chỉ khi nó
  nằm trong một dải mà AWS, Azure hoặc ASN của game công bố. Route được cài khi
  game chạy và gỡ khi game thoát.
- **Khi có lỗi, quay về đường bình thường.** Nếu relay ngừng phản hồi, client
  chuyển sang relay khác. Nếu không relay nào phản hồi, client gỡ route của
  mình. Adapter tunnel và mọi route biến mất khi service dừng. Khởi động lại máy
  là máy trở về nguyên trạng hoàn toàn.

## Thành phần

| Lệnh | Chạy trên | Làm gì |
|---|---|---|
| `gnl-service` | Windows, LocalSystem | Giữ tunnel. Lấy session và profile, đo các relay, cài và gỡ route. |
| `gnl-ui` | Windows, dưới quyền người dùng | Icon ở tray, panel mini và cửa sổ đầy đủ. Chỉ yêu cầu service một trong các lệnh `connect`, `disconnect`, `status`, `reload-profile`, ngoài ra không gì khác. |
| `gnl-control` | Linux | Control plane: contributor key, slot thiết bị, đăng ký và đồng bộ relay, game profile. Đồng thời tạo key (`-mint-key`). |
| `gnl-agent` | Relay Linux | Đồng bộ WireGuard peer, egress allowlist (ipset) và firewall theo control plane. |
| `gnl-relaycheck` | Bất kỳ đâu bên ngoài relay | Chứng minh cổng UDP của relay truy cập được từ Internet, và báo kết quả cho control plane. |
| `gnl-profile` | Người vận hành | Dựng danh sách CIDR của một game từ các địa chỉ quan sát được và các dải đã công bố. Chỉ chạy thử (dry-run) trừ khi có `-publish`. |
| `gnl-probe`, `gnl-analyze` | Máy đo | Chiến dịch đo chất lượng route P0: vào giờ cao điểm, đường qua VPS có tốt hơn đường của nhà mạng không? |

## Cấu trúc repository

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

## Phát triển

Cần Go 1.25 trở lên. Phần lớn code build và test được trên mọi hệ điều hành.
Các phần chỉ dành cho Windows nằm sau build tag, còn phần logic ra quyết định
của chúng được đặt trong các file không phụ thuộc nền tảng nên được test ở mọi
nơi.

```bash
go test ./...                       # everything that runs anywhere
GOOS=windows go vet ./...           # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh   # iptables/ipset against a real kernel (needs Docker)
```

Các test firewall thay đổi firewall của máy. Chúng nằm sau build tag
`linuxroot` và `GNL_FIREWALL_TESTS=1`, nên `go test ./...` không bao giờ đụng
tới iptables. Script trên chạy chúng trong một container đặc quyền dùng xong
bỏ.

### Build

```bash
# Windows client
GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o gnl-ui.exe ./cmd/gnl-ui

# relay and control plane
GOOS=linux GOARCH=amd64 go build -o gnl-agent ./cmd/gnl-agent
GOOS=linux GOARCH=amd64 go build -o gnl-control ./cmd/gnl-control
```

`gnl-ui.exe` phải được phát hành kèm `deploy/windows/gnl-ui.exe.manifest` đặt
ngay cạnh nó. Thiếu manifest thì menu ở tray không được tạo.

### CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) (tiếng Anh) chạy mỗi lần
push lên `main` và với mọi pull request:

1. `gofmt`, `go vet` và `go test` trên Ubuntu và trên Windows. Trên Ubuntu, nó
   còn vet bản build Windows.
2. Các test firewall trên kernel thật.
3. Khi cả hai bước trên đều qua, nó build các binary Linux và gói client
   Windows (cả hai file thực thi, manifest và các script cài đặt). Cả hai đều
   tải được từ artifact của lần chạy.

### Quy ước

- Commit theo Conventional Commits (`feat(client): …`, `fix(relay): …`), kèm
  phần thân giải thích lý do.
- Logic có ra quyết định thì phải có test. Code chỉ gọi xuống hệ điều hành được
  giữ thật mỏng, để phần không được test càng ít càng tốt.
- Comment giải thích tại sao, không giải thích cái gì.

## Mô hình bảo mật, tóm tắt

- **Một ranh giới đặc quyền duy nhất trên client.** UI chạy không có đặc quyền
  và chỉ được yêu cầu service bốn lệnh không tham số. Nó không thể chỉ định một
  relay, một route, một file hay một lệnh. Named pipe chỉ nhận người dùng đang
  đăng nhập tương tác.
- **Bí mật được lưu dưới dạng hash.** Contributor key và relay token chỉ được
  lưu dưới dạng hash. Private key của thiết bị nằm trong
  `C:\ProgramData\GameNoLag`, chỉ SYSTEM và Administrators đọc được.
- **Relay không phải open proxy.** Chính sách FORWARD là DROP. Egress bị giới
  hạn trong các dải địa chỉ game đã công bố, và mỗi session bị giới hạn 64 KB/s
  mỗi chiều. Relay mới không mang lưu lượng nào cho tới khi khả năng truy cập
  của nó được chứng minh từ bên ngoài và thời gian theo dõi của nó đã qua.
- **Không bao giờ đụng vào game.** Game được nhận diện bằng tên tiến trình,
  giống cách Task Manager làm. Client không bao giờ mở handle vào game, đọc bộ
  nhớ của game hay inject bất cứ thứ gì.
- **Profile cần bằng chứng.** Dữ liệu quan sát chỉ ghi địa chỉ đích, và một
  contributor không thể một mình đưa một địa chỉ vào profile.

## Tài liệu

Tài liệu đầy đủ có bằng [English](docs/en/README.md),
[Tiếng Việt](docs/vi/README.md) và [简体中文](docs/zh-CN/README.md).

| Hướng dẫn | Dành cho |
|---|---|
| [Hướng dẫn sử dụng](docs/vi/user-guide.md) | Người chơi: cài đặt, sử dụng, xử lý sự cố, gỡ cài đặt |
| [Cài đặt client](docs/vi/setup-client.md) | Cài đặt, cấu hình và đóng gói client Windows |
| [Cài đặt relay](docs/vi/setup-relay.md) | Contributor chạy relay trên VPS |
| [Cài đặt control plane](docs/vi/setup-control-plane.md) | Người vận hành: Postgres, TLS, key, xác minh relay, game profile |
| [Kiến trúc](docs/vi/architecture.md) | Các phần ghép với nhau thế nào, API, vòng đời relay, ranh giới bảo mật |

Tài liệu tham khảo chuyên sâu (tiếng Anh):
[runbook client Windows](docs/windows-client-runbook.md),
[runbook P1](docs/p1-runbook.md) và [runbook P0](docs/p0-runbook.md).

## Đóng góp

Mọi đóng góp đều được hoan nghênh. Hãy đọc [CONTRIBUTING.md](CONTRIBUTING.md)
(tiếng Anh) trước, và báo cáo lỗ hổng bảo mật một cách riêng tư theo hướng dẫn
trong [SECURITY.md](SECURITY.md) (tiếng Anh), không mở issue công khai. Mọi
người tham gia đều tuân theo
[Bộ quy tắc ứng xử](CODE_OF_CONDUCT.md) (tiếng Anh).

## Giấy phép

[Apache License 2.0](LICENSE) (tiếng Anh).
