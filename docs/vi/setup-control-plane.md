# Cài đặt control plane

[English](../en/setup-control-plane.md) · **Tiếng Việt** · [简体中文](../zh-CN/setup-control-plane.md)

Control plane (`gnl-control`) lưu contributor key, slot thiết bị, bản ghi relay
và các game profile đã công bố. Nó **không** nằm trên đường dữ liệu. Gói tin
của người chơi không bao giờ đi qua nó, nên nó có thể chạy ở bất kỳ đâu có một
địa chỉ HTTPS công khai, và độ trễ từ nó tới người chơi không quan trọng.

Bạn cần:

- Một máy Linux, bản phân phối nào cũng được miễn là có systemd.
- PostgreSQL 13 trở lên.
- Một tên miền và một chứng chỉ TLS. `gnl-control` chỉ nói HTTP thường và cần
  một reverse proxy đặt phía trước.
- Các binary `gnl-control` và `gnl-profile`: lấy từ artifact CI
  `gamenolag-linux-amd64`, hoặc tự build từ mã nguồn (xem bên dưới).

## 1. Build hoặc tải các binary

```bash
git clone https://github.com/hashcott/NoLag.git && cd NoLag
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-control ./cmd/gnl-control
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o gnl-profile ./cmd/gnl-profile
sudo install -m 0755 gnl-control gnl-profile /usr/local/bin/
```

## 2. Tạo database

```bash
sudo -u postgres createuser --pwprompt gamenolag
sudo -u postgres createdb --owner gamenolag gamenolag
```

Schema được áp dụng mỗi lần `gnl-control` khởi động. Mọi câu lệnh đều
idempotent (chạy lại nhiều lần vẫn cho cùng kết quả), nên không có bước
migration riêng.

## 3. Chạy dưới systemd

`/etc/gnl/control.env`, quyền `0600`, chủ sở hữu root:

```bash
GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
```

`/etc/systemd/system/gnl-control.service`:

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

### Flag

| Flag | Mặc định | Ý nghĩa |
|---|---|---|
| `-dsn` | `$GNL_DSN` | Chuỗi kết nối Postgres. Bắt buộc |
| `-listen` | `:8080` | Địa chỉ lắng nghe HTTP. Bind vào loopback khi có proxy phía trước |
| `-trust-proxy` | off | Dùng `X-Forwarded-For` để rate limit. **Chỉ bật khi proxy của bạn thật sự đặt header đó.** Nếu không, mỗi client sẽ tự chọn bucket rate limit của mình |
| `-stale-after` | `5m` | Đánh dấu relay `down` sau khoảng thời gian này không có sync |
| `-poll-secs` | `10` | Tần suất yêu cầu relay sync |
| `-mint-key` | — | Tạo một contributor key, in ra, rồi thoát |

## 4. Đặt TLS phía trước

Relay token và contributor key được gửi đi dưới dạng bearer token. Qua HTTP
thường, bất kỳ ai trên đường truyền cũng có thể sao chép chúng. Cả hai
installer đều từ chối URL control không phải `https`.

Một cấu hình Caddy tối thiểu tự lấy và tự gia hạn chứng chỉ:

```
cp.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Với nginx, hãy chuyển tiếp địa chỉ client để `-trust-proxy` đếm đúng bên gọi
thật:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

Kiểm tra từ bên ngoài: `curl -s https://cp.example.com/healthz`.

## 5. Tạo contributor key

```bash
sudo sh -c 'set -a; . /etc/gnl/control.env; exec /usr/local/bin/gnl-control -mint-key'
```

Key chỉ được in ra **một lần**. Chỉ hash của nó được lưu, nên không thể khôi
phục lại. Hãy gửi key cho contributor qua một kênh bạn tin cậy. Mặc định một
key kích hoạt được tối đa ba thiết bị; giá trị này là
`contributor_key.max_devices` trong database.

## 6. Xác minh từng relay từ bên ngoài

Relay mới ở trạng thái `pending` và không bao giờ được cung cấp cho người chơi
cho tới khi một phép kiểm tra từ bên ngoài relay chứng minh cổng UDP của nó
đang mở. Xem
[Cài đặt relay → Xác minh từ bên ngoài](setup-relay.md#5-verify-from-outside).

Các trạng thái relay:

| Trạng thái | Ý nghĩa | Được cung cấp cho người chơi |
|---|---|---|
| `pending` | Đã đăng ký, chưa từng được xác minh từ bên ngoài | không |
| `up` | Đã xác minh truy cập được và vẫn đang sync | có, sau khi qua `trusted_after` (48 h kể từ lúc đăng ký) |
| `unreachable` | Một phép kiểm tra từ bên ngoài thất bại; lý do được lưu lại | không |
| `down` | Không có sync trong khoảng `-stale-after` | không |

```sql
SELECT id, region, status, active_peers, capacity, last_seen, trusted_after,
       unreachable_detail
  FROM relay ORDER BY status, id;
```

<a id="7-publish-a-game-profile"></a>
## 7. Công bố game profile

Khi chưa có profile nào, egress allowlist của mọi relay đều trống và relay
không chuyển tiếp gì cả. Đó là trạng thái khởi đầu có chủ đích.

Profile được dựng từ những gì contributor quan sát được, đối chiếu chéo với
các dải mà nhà cung cấp cloud công bố. Hãy chạy không có `-publish` trước. Khi
đó nó in ra những gì sẽ công bố và không thay đổi gì.

```bash
export GNL_DSN=postgres://gamenolag:<password>@127.0.0.1:5432/gamenolag
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1
# review the list, then:
gnl-profile -game valorant -aws-regions ap-southeast-1,ap-northeast-1 -publish
```

| Flag | Ý nghĩa |
|---|---|
| `-game` | Id của game, đúng như trong dữ liệu quan sát. Bắt buộc |
| `-aws-regions` | Các region AWS có dải đã công bố được tính |
| `-azure-regions`, `-azure-url` | Các region Azure, và URL của file JSON Service Tags hiện hành |
| `-asns` | Các ASN có prefix đã announce được tính |
| `-min-reporters` | Số key độc lập mà một địa chỉ cần có (mặc định 3) |
| `-publish` | Công bố thật. Không có flag này thì không ghi gì cả |

Các địa chỉ không khớp dải đã công bố nào được liệt kê là unverified (chưa xác
minh) và không bao giờ được thêm vào. Chúng thường là voice chat hoặc CDN, và
nếu thêm tay thì mọi thứ khác trong cùng khối cũng bị route theo.

Relay nhận profile mới trong vòng một lần sync. Client nhận nó ở lần connect
tiếp theo, hoặc khi bấm *Refresh game list*.

## Vận hành hằng ngày

| Việc | Cách làm |
|---|---|
| Xem toàn bộ relay | Câu `SELECT` ở bước 6 |
| Giải phóng một slot thiết bị | Contributor gọi `DELETE /v1/devices/{id}` bằng key của họ, hoặc chạy `DELETE FROM device WHERE id = '…';` |
| Thu hồi một key | `UPDATE contributor_key SET status = 'revoked' WHERE key_hash = '…';` rồi `DELETE FROM device WHERE key_hash = '…';` Hash tính bằng `printf %s 'GNL-…' \| sha256sum`. **Cần cả hai bước:** key đã thu hồi không thể đăng ký relay hay kích hoạt thiết bị mới, nhưng các thiết bị nó đã kích hoạt vẫn tiếp tục nhận session cho tới khi các dòng của chúng bị xoá |
| Xoá một relay | `DELETE FROM relay WHERE id = '…';` Các peer binding của nó bị xoá theo |
| Log | `journalctl -u gnl-control -f` |
| Sao lưu | `pg_dump gamenolag`. Database là toàn bộ trạng thái; binary không giữ trạng thái |

## Tự build các binary

Cần Go 1.25 trở lên:

```bash
go test ./...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

Để hiểu sâu hơn về công việc của người vận hành — relay đầu tiên, gắn peer
bằng tay, mỗi phép kiểm tra chứng minh điều gì — xem
[runbook P1](../p1-runbook.md) (tiếng Anh).
