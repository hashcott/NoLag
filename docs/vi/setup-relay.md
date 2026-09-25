# Cài đặt relay

[English](../en/setup-relay.md) · **Tiếng Việt** · [简体中文](../zh-CN/setup-relay.md)

Relay là một VPS mang lưu lượng game của người chơi tới máy chủ game. Nó do một
thành viên cộng đồng (một *contributor*) đóng góp, và người đó nhận lại một
contributor key. Mọi thứ relay chuyển tiếp đều bị giới hạn trong các dải địa
chỉ game đã công bố và bị giới hạn tốc độ theo từng người chơi, nên không thể
dùng nó làm proxy đa dụng.

## Yêu cầu

| | |
|---|---|
| Ảo hoá | **KVM**. OpenVZ và LXC không tạo được thiết bị TUN; installer sẽ dừng nếu thiếu `/dev/net/tun` |
| Kernel | 5.6 trở lên (WireGuard có sẵn trong kernel từ 5.6) |
| Hệ điều hành | Linux dùng systemd, có `apt-get`, `dnf` hoặc `yum` (Debian, Ubuntu, Fedora, họ RHEL) |
| Vị trí | Gần máy chủ game: ưu tiên Singapore, sau đó Tokyo |
| Cấu hình | Gói nhỏ nhất là đủ. Một session game dùng khoảng 10 KB/s |
| Mạng | Một địa chỉ IPv4 công khai, và UDP 51820 mở trong firewall của nhà cung cấp |
| Từ người vận hành | Một contributor key `GNL-XXXX-XXXX-XXXX-XXXX` và URL của control plane |

## 1. Lấy installer và agent

Đặt `relay-v1.sh` và `gnl-agent` cạnh nhau. Chúng lấy từ repository
(`deploy/relay-v1.sh`) và artifact CI `gamenolag-linux-amd64`, hoặc từ bất cứ
nơi nào người vận hành công bố kèm các file `.sha256`:

```bash
sha256sum -c relay-v1.sh.sha256 gnl-agent.sha256
less relay-v1.sh          # it runs as root: read it first
chmod +x relay-v1.sh gnl-agent
```

Để tự build `gnl-agent`:
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gnl-agent ./cmd/gnl-agent`.

## 2. Cài đặt

```bash
sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX \
                   --control https://cp.example.com \
                   --region sgp
```

| Tuỳ chọn | Mặc định | Ý nghĩa |
|---|---|---|
| `--key` | — | Contributor key. Bắt buộc |
| `--control` | — | URL của control plane, chỉ nhận `https`. Bắt buộc |
| `--region` | — | Nhãn tự do hiển thị cho người vận hành, ví dụ `sgp`, `tyo` |
| `--endpoint IP` | địa chỉ WAN | Địa chỉ công khai mà người chơi kết nối tới. Cần khi nhà cung cấp NAT VPS |
| `--port N` | `51820` | Cổng UDP của WireGuard |
| `--iface NAME` | `wg0` | Tên interface WireGuard |
| `--rate RATE` | `64kb/s` | Giới hạn mỗi session theo mỗi chiều |
| `--uninstall` | — | Gỡ mọi thứ script đã cài |

Những gì nó làm trên máy:

- Cài `wireguard-tools`, `ipset` và `iptables` nếu còn thiếu.
- Tạo interface WireGuard và một private key tại `/etc/gnl/relay.key`
  (quyền `0600`). Key không bao giờ rời khỏi máy.
- Đặt `net.ipv4.ip_forward=1` và `rp_filter=2`.
- Thêm năm rule iptables và một ipset (`gnl-games`).
- Cài `/usr/local/bin/gnl-agent` và hai unit systemd:
  - `gnl-wg.service` tạo lại interface mỗi lần khởi động.
  - `gnl-agent.service` sync với control plane mỗi 10 s.

Nếu nó dừng vì interface WAN có địa chỉ private, nghĩa là nhà cung cấp đang NAT
VPS. Chạy lại với `--endpoint <public-ip>`. Relay đăng ký bằng địa chỉ private
sẽ không bao giờ nhận được handshake nào, và log của chính nó cũng không nói lý
do.

## 3. Mở cổng UDP ở phía nhà cung cấp

Trong trang quản trị của nhà cung cấp, mở **UDP 51820** (hoặc cổng `--port` của
bạn) trong security group hoặc firewall của VPS. Installer không thể kiểm tra
điều này từ bên trong máy, và đây là lý do phổ biến nhất khiến một relay trông
có vẻ ổn khi xem tại chỗ nhưng không truy cập được từ bất kỳ nơi nào khác.

## 4. Kiểm tra tại chỗ

```bash
systemctl status gnl-wg gnl-agent
wg show                          # interface, port, peers
journalctl -u gnl-agent -n 20    # a sync line every 10 s
iptables -L FORWARD -n -v --line-numbers | head
ipset list gnl-games             # empty until a game profile is published
```

Hai rule DROP `hashlimit` phải nằm **trên** các rule ACCEPT trong `FORWARD`;
nếu nằm dưới thì chúng không bao giờ khớp. Chính sách FORWARD phải là `DROP`.

Khởi động lại máy một lần rồi kiểm tra lại cả hai unit. Interface WireGuard
không tự tồn tại qua lần khởi động lại; `gnl-wg` tạo lại nó.

<a id="5-verify-from-outside"></a>
## 5. Xác minh từ bên ngoài

Từ **một máy khác**, không phải chính relay (cần quyền root để tạo interface
tạm):

```bash
sudo gnl-relaycheck -endpoint <relay-ip>:51820 \
                    -pubkey "$(ssh relay 'sudo wg pubkey < /etc/gnl/relay.key')" \
                    -control https://cp.example.com -key GNL-XXXX-XXXX-XXXX-XXXX
```

- `REACHABLE`: relay được đánh dấu `up`.
- `NOT REACHABLE`: in ra các nguyên nhân có thể, đầu tiên là firewall của nhà
  cung cấp.

`-control` và `-key` là thứ ghi lại kết luận. Thiếu chúng, bạn vẫn thấy câu trả
lời, nhưng control plane không bao giờ biết, và relay vẫn ở `pending`. Key
chứng minh báo cáo đến từ chủ của relay, nên không ai có thể đánh sập relay của
người khác.

Relay vừa được xác minh chỉ được cung cấp cho người chơi sau thời gian theo dõi
(48 giờ kể từ lúc đăng ký). Hãy chạy lại phép kiểm tra sau bất cứ điều gì có
thể thay đổi đường vào: khởi động lại, thay đổi firewall, đổi địa chỉ IP.

## Những gì bảo vệ contributor

- **Egress allowlist.** Lưu lượng chuyển tiếp chỉ được đi tới các dải game đã
  công bố trong `gnl-games`. Mọi thứ khác bị drop.
- **Giới hạn tốc độ.** 64 KB/s mỗi session mỗi chiều, khoảng sáu lần mức chơi
  bình thường. Đây là giới hạn tốc độ, không phải hạn mức theo tháng; cùng với
  allowlist, nó giữ tổng lượng sử dụng ở mức nhỏ.
- **Không có service nhận kết nối vào.** Chỉ cổng UDP của WireGuard được dùng.
- **Thu hồi có hiệu lực ngay.** Xoá một peer trên control plane sẽ gỡ nó khỏi
  kernel trong vòng một lần sync.

## Gỡ cài đặt

```bash
sudo ./relay-v1.sh --uninstall
```

Lệnh này gỡ các unit, interface, binary, các rule firewall, ipset và file
sysctl, đồng thời khôi phục chính sách FORWARD trước đó. Nó cố ý **giữ lại
`/etc/gnl`**, nơi chứa private key của relay. Hãy tự xoá thư mục này khi bạn
dùng xong (`sudo rm -rf /etc/gnl`), và nhờ người vận hành xoá bản ghi relay.

## Xử lý sự cố

| Hiện tượng | Nguyên nhân | Cách khắc phục |
|---|---|---|
| Installer: `/dev/net/tun is missing` | OpenVZ hoặc LXC | Dùng VPS KVM |
| Installer: kernel cũ hơn 5.6 | Image cũ | Nâng cấp kernel hoặc chọn image mới hơn |
| Installer từ chối địa chỉ WAN private | Nhà cung cấp NAT | `--endpoint <public-ip>` |
| `gnl-relaycheck`: NOT REACHABLE | Firewall của nhà cung cấp đang đóng | Mở UDP 51820 ở phía nhà cung cấp |
| Relay mãi ở `pending` | Phép kiểm tra chưa từng được báo lên | Chạy lại `gnl-relaycheck` với `-control` và `-key` |
| Relay chuyển sang `down` | Không có sync trong 5 phút | `systemctl status gnl-agent`, `journalctl -u gnl-agent` |
| `gnl-wg` lỗi sau khi khởi động lại | Thiếu `/etc/gnl/relay.state` hoặc `relay.key` | Chạy lại installer |

Để hiểu đầy đủ lý do đằng sau từng bước, xem [runbook P1](../p1-runbook.md)
(tiếng Anh).
