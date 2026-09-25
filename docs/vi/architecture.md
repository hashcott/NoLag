# Kiến trúc

[English](../en/architecture.md) · **Tiếng Việt** · [简体中文](../zh-CN/architecture.md)

GameNoLag có ba phần: một **client** trên PC Windows của người chơi, các
**relay** trên VPS do cộng đồng đóng góp, và một **control plane** duy nhất biết
ai được dùng relay nào. Lưu lượng game đi theo đường client → relay → máy chủ
game. Control plane không bao giờ nằm trên đường đó.

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

## Các vai trò

| Vai trò | Có gì | Làm gì |
|---|---|---|
| **Người vận hành (operator)** | Control plane và database của nó | Tạo contributor key, xác minh relay, công bố game profile |
| **Người đóng góp (contributor)** | Một VPS KVM và một contributor key | Chạy một relay; dùng key trên tối đa ba PC của chính mình |
| **Người chơi (player)** | Một PC Windows có cài client | Chơi game; client lo phần còn lại |

Trong giai đoạn hiện tại, mọi người chơi đều là contributor: key đã đăng ký
relay cũng chính là key kích hoạt thiết bị.

## Thành phần

| Binary | Chạy trên | Vai trò |
|---|---|---|
| `gnl-control` | Linux, sau TLS | HTTP API và kho dữ liệu Postgres: key, thiết bị, relay, peer binding, game profile |
| `gnl-agent` | Mỗi relay | Cứ 10 s một lần: báo trạng thái, nhận peer và dải địa chỉ game, đồng bộ WireGuard, ipset và iptables |
| `gnl-relaycheck` | Bất kỳ máy nào bên ngoài relay | Thực hiện một handshake WireGuard thật từ Internet. Là thứ duy nhất đánh dấu relay `up` |
| `gnl-profile` | Máy của người vận hành | Dựng danh sách CIDR của một game từ dữ liệu quan sát của contributor, đối chiếu chéo với các dải cloud đã công bố |
| `gnl-service` | Windows, LocalSystem | Giữ tunnel: session, đo đạc, route, nhận diện game, failover |
| `gnl-ui` | Windows, dưới quyền người dùng | Icon ở tray và cửa sổ. Không có đặc quyền |
| `gnl-probe`, `gnl-analyze` | Máy đo | Chiến dịch P0 nhằm xác định liệu có đường VPS nào tốt hơn nhà mạng vào giờ cao điểm hay không |

## API của control plane

Mọi endpoint đều là JSON qua HTTPS. Thông tin xác thực là bearer token.

| Endpoint | Bên gọi | Mục đích |
|---|---|---|
| `POST /v1/relay/register` | Installer của relay, kèm contributor key | Đăng ký relay; trả về id, token và subnet bên trong của relay |
| `POST /v1/relay/sync` | `gnl-agent`, kèm relay token của nó | Báo trạng thái; nhận peer, CIDR của game và chu kỳ poll |
| `POST /v1/relay/reachability` | `gnl-relaycheck`, kèm key của chủ relay | Ghi lại việc relay có truy cập được từ bên ngoài hay không |
| `POST /v1/activate` | Client, kèm contributor key | Gắn public key của thiết bị này vào một slot |
| `GET /v1/session` | Client | Các relay được cung cấp cho thiết bị này: `up`, đã qua `trusted_after` |
| `GET /v1/profile` | Client | Danh sách CIDR game mới nhất đã công bố |
| `DELETE /v1/devices/{id}` | Contributor | Giải phóng một slot thiết bị |
| `POST /v1/observations` | Công cụ của contributor | Các địa chỉ đích được thấy đang mang lưu lượng của một game |
| `GET /healthz` | Giám sát | Kiểm tra còn sống (liveness) |

Các endpoint dùng key bị rate limit ở mức 20 request mỗi giờ cho mỗi địa chỉ
nguồn và cho mỗi tiền tố key.

## Vòng đời relay

```
register ──► pending ──(gnl-relaycheck: reachable)──► up ──(no sync for 5 min)──► down
                │                                     │                           │
                └──(gnl-relaycheck: not reachable)──► unreachable ◄───────────────┘
                                                      (a later check can move it back to up)
```

Relay chỉ được cung cấp cho người chơi khi nó ở trạng thái `up` **và** đã qua
thời điểm `trusted_after`. Một lần sync chứng minh relay kết nối được tới
control plane. Nó không chứng minh người chơi kết nối được tới relay: security
group của nhà cung cấp nằm chắn trước cổng UDP và không thể nhìn thấy từ bên
trong máy. Chỉ một handshake từ bên ngoài mới trả lời được câu hỏi đó.

## Một lần connect, từng bước

1. Client lấy profile (CIDR của game). Nếu control plane không hoạt động nhưng
   client đã có sẵn một profile, nó tiếp tục dùng profile đó.
2. Client lấy session. Mã `403` nghĩa là thiết bị chưa được kích hoạt, nên nó
   kích hoạt rồi hỏi lại.
3. Client đọc default route hiện tại. Việc này diễn ra trước khi adapter tunnel
   tồn tại, nên tunnel không thể bị nhầm là default route.
4. Client phân giải mọi endpoint của relay thành một địa chỉ IPv4 cụ thể, một
   lần duy nhất.
5. Client tạo một adapter Wintun, thêm mọi relay làm WireGuard peer không có
   allowed-ips, và handshake với từng relay. Chính handshake là phép đo, được
   thực hiện qua đường mạng của người chơi.
6. Client chọn relay nhanh nhất và ghim `/32` của relay đó qua adapter vật lý,
   để các gói tin của chính tunnel không bao giờ đi vào tunnel. Route của game
   chỉ được cài khi có một tiến trình game đã biết đang chạy.

Sau đó:
- Client xếp hạng lại mỗi năm phút, nhưng chỉ giữa các trận, và chỉ chuyển
  relay khi nhanh hơn ít nhất 10 ms.
- Relay không có handshake trong 150 s bị coi là đã chết. Client chuyển sang
  relay khác. Nếu không relay nào phản hồi, client gỡ route của mình.

## Game profile

Việc định tuyến dựa trên địa chỉ đích, nên profile phải chỉ ra địa chỉ nào
thuộc về một game, và phải giữ cho thật hẹp. Các nhà cung cấp cloud công bố
các khối /17 và /18; route cả một khối sẽ kéo theo các dịch vụ không liên quan
đi qua relay.

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

Một khối anycast như AWS Global Accelerator được giữ nguyên là một `/32` duy
nhất, không bao giờ nới rộng. Khối đó không cho biết ai đứng sau nó.

## Ranh giới bảo mật

| Ranh giới | Biện pháp phòng vệ |
|---|---|
| UI không đặc quyền → service LocalSystem | Named pipe chỉ mở cho SYSTEM, Administrators và người dùng INTERACTIVE. Đúng bốn lệnh, không tham số, độ dài dòng có giới hạn |
| Binary của service và trạng thái trên đĩa | `Program Files\GameNoLag` chỉ Administrators ghi được. `ProgramData\GameNoLag` chỉ SYSTEM và Administrators đọc được |
| Thông tin xác thực khi lưu trữ | Contributor key và relay token được lưu dưới dạng hash SHA-256 |
| Thông tin xác thực khi truyền đi | Các installer từ chối URL control không phải HTTPS |
| Relay bị dùng làm open proxy | Chính sách FORWARD là DROP. Egress chỉ tới các dải game đã công bố (ipset). 64 KB/s mỗi session mỗi chiều |
| Một client nhận lưu lượng của client khác | `UNIQUE (relay_id, inner_ip)` trong database |
| Relay mới hút lưu lượng | Ở trạng thái `pending` cho tới khi được xác minh từ bên ngoài, và không được cung cấp trước `trusted_after` |
| Một người đầu độc profile | Việc đưa vào profile cần ba key khác nhau và phải khớp với một dải đã công bố |
| Anti-cheat | Game chỉ được nhận diện qua tên tiến trình; không có gì mở, đọc hay hook vào tiến trình game |

## Dữ liệu được lưu

| Ở đâu | Cái gì |
|---|---|
| Control plane | Hash của key, bản ghi và bộ đếm của relay, public key và dấu vân tay (hostname) của thiết bị, peer binding, các profile đã công bố, `ip:port` đích quan sát được theo từng game và hash của key |
| Relay | WireGuard private key của nó (`/etc/gnl/relay.key`, 0600), token của nó, các WireGuard peer |
| Client | Private key của thiết bị, cấu hình, một file log xoay vòng 8 MB |

Không nơi nào ghi lại nội dung gói tin (payload), cũng như địa chỉ nguồn của
lưu lượng game của người chơi.
