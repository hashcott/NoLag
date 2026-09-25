# GameNoLag

**Ping ổn định hơn cho người chơi ở Việt Nam, nhờ relay do cộng đồng vận hành đặt ngay cạnh máy chủ game.**

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[English](README.md) · **Tiếng Việt** · [简体中文](README.zh-CN.md)

Người chơi ở Việt Nam thường được ghép vào máy chủ game ở Singapore, và sang
Tokyo khi matchmaker quá tải. Buổi tối, đường đi của nhà mạng tới đó hay bị
nghẽn: ping nhảy và mất gói ngay giữa trận. Một VPS ở Singapore thuộc nhà cung
cấp khác, đi tuyến transit khác, thường tới được cùng các máy chủ đó qua một
đường sạch hơn.

GameNoLag đặt đường đó ngay dưới game của bạn. Nó cho **chỉ lưu lượng của game**
đi qua một relay WireGuard gần máy chủ game. Relay được chọn bằng cách đo chính
kết nối của bạn, và GameNoLag rút ra ngay khi có sự cố. Các relay là VPS do
cộng đồng đóng góp.

> **Trạng thái: giai đoạn đầu, chưa tới tay người chơi.** Mọi thành phần đã
> được build và test trong CI, trên Linux và Windows, với kernel và database
> thật. Client Windows chưa được người chơi nào chạy thử; trước đó cần làm
> [checklist chạy tay trên Windows](docs/windows-client-runbook.md#the-window)
> (tiếng Anh).

## Khác biệt ở đâu

- **Chỉ game đi qua.** Đây không phải VPN. Route tới máy chủ của một game chỉ
  được cài khi game đó đang chạy, và bị gỡ khi game thoát. Trình duyệt, Discord
  và tải file không bao giờ rời khỏi kết nối bình thường của bạn.
- **Đường của bạn quyết định.** Mỗi relay ứng viên được đo bằng một lần
  handshake WireGuard thật từ máy bạn, relay nhanh nhất được chọn. GameNoLag
  không đổi relay giữa trận chỉ vì nhanh hơn một chút.
- **Không đụng vào game.** Game được nhận ra bằng tên tiến trình, giống cách
  Task Manager liệt kê. Không có gì mở, đọc hay chèn vào tiến trình game.
- **Lỗi thì an toàn.** Relay chết thì client chuyển sang relay khác. Không
  relay nào phản hồi thì client gỡ route, và game của bạn tiếp tục chạy qua nhà
  mạng. Dừng service hoặc khởi động lại máy thì không còn sót lại gì.
- **Relay không bị lạm dụng.** Relay chỉ chuyển tiếp tới các dải địa chỉ game
  đã công bố, có giới hạn tốc độ cho từng người chơi. Relay được kiểm tra từ bên
  ngoài trước khi đưa vào dùng. Khi một contributor bị thu hồi key, họ bị loại
  khỏi mạng trong vòng một lần sync.

## Cách hoạt động

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

Control plane quyết định ai được dùng relay nào và công bố các dải địa chỉ của
game. Nó không bao giờ nằm trên đường đi của gói tin. Dải địa chỉ game được giữ
hẹp: một địa chỉ chỉ được thêm khi ba contributor độc lập đã thấy nó, và chỉ khi
nó nằm trong một dải mà AWS, Azure hoặc mạng của chính game công bố.

Chi tiết hơn: [Kiến trúc](docs/vi/architecture.md).

## Bắt đầu

| Tôi muốn… | Bắt đầu từ |
|---|---|
| **Chơi game** với ping thấp và ổn định hơn | [Hướng dẫn sử dụng](docs/vi/user-guide.md) |
| **Đóng góp một VPS** làm relay | [Cài đặt relay](docs/vi/setup-relay.md) |
| **Vận hành một hệ thống** cho cộng đồng | [Cài đặt control plane](docs/vi/setup-control-plane.md), rồi làm theo [thứ tự dựng hệ thống](docs/vi/README.md) |
| **Cài hoặc đóng gói** client Windows | [Cài đặt client](docs/vi/setup-client.md) |
| **Phát triển** | [Phát triển](#phát-triển) và [CONTRIBUTING](CONTRIBUTING.md) (tiếng Anh) |

Người chơi cần một contributor key từ người vận hành hệ thống. Ở giai đoạn này,
key được cấp cho người đóng góp relay, và mỗi key dùng được trên tối đa ba máy
của chính họ.

## Gồm những gì

| Binary | Chạy trên | Vai trò |
|---|---|---|
| `gnl-service` | Windows, dạng service | Giữ tunnel, đo relay, định tuyến cho game đang chạy |
| `gnl-ui` | Windows, dưới quyền người dùng | Icon khay hệ thống, panel mini và cửa sổ đầy đủ. Không có đặc quyền |
| `gnl-control` | Linux | Control plane: key, slot thiết bị, relay, profile game |
| `gnl-agent` | Mỗi relay | Đồng bộ peer WireGuard, allowlist đầu ra và firewall |
| `gnl-relaycheck` | Bên ngoài relay | Chứng minh relay truy cập được từ Internet |
| `gnl-profile` | Người vận hành | Dựng danh sách địa chỉ của một game từ observation và các dải đã công bố |
| `gnl-probe`, `gnl-analyze` | Máy đo | Đo xem đường qua VPS có tốt hơn nhà mạng vào giờ cao điểm không |

## Phát triển

Cần Go 1.25 trở lên. Test với kernel và database cần Docker.

```bash
go test ./...                               # runs anywhere
GOOS=windows go vet ./...                   # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh       # relay firewall against a real kernel
GNL_TEST_DSN=postgres://… go test ./internal/control/   # control-plane store against Postgres
```

Code chỉ dành cho Windows nằm sau build tag, còn phần quyết định của nó nằm
trong các file không phụ thuộc nền tảng, nên gần như mọi thứ test được trên bất
kỳ máy nào. CI chạy tất cả các lệnh trên ở mỗi lần push và mỗi pull request, và
build sẵn binary Linux cùng gói cài Windows để tải về.

Lệnh build, quy ước và những ranh giới không được nới lỏng nằm trong
[CONTRIBUTING.md](CONTRIBUTING.md) (tiếng Anh).

## Bảo mật

Client chỉ có một ranh giới đặc quyền. Tray nói chuyện với service qua một
named pipe chỉ nhận bốn lệnh không tham số, và chỉ từ người dùng đang đăng
nhập. Key và token chỉ được lưu dưới dạng hash. Relay loại bỏ mọi thứ không đi
tới một dải địa chỉ game.

Danh sách đầy đủ nằm trong [Kiến trúc](docs/vi/architecture.md). Báo lỗ hổng
một cách riêng tư theo hướng dẫn trong [SECURITY.md](SECURITY.md) (tiếng Anh).

## Tài liệu

Hướng dẫn bằng [English](docs/en/README.md), [Tiếng Việt](docs/vi/README.md) và
[简体中文](docs/zh-CN/README.md): hướng dẫn sử dụng, cài client, relay và control
plane, kiến trúc.

Runbook chuyên sâu (tiếng Anh):
- [client Windows](docs/windows-client-runbook.md)
- [control plane và relay đầu tiên](docs/p1-runbook.md)
- [đo chất lượng đường đi](docs/p0-runbook.md)

## Đóng góp

Hoan nghênh issue và pull request. Vui lòng đọc
[CONTRIBUTING.md](CONTRIBUTING.md) (tiếng Anh) trước. Mọi người tham gia đều
tuân theo [Quy tắc ứng xử](CODE_OF_CONDUCT.md) (tiếng Anh).

## Giấy phép

[Apache License 2.0](LICENSE).
