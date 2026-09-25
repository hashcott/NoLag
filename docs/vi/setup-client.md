# Cài đặt client Windows

[English](../en/setup-client.md) · **Tiếng Việt** · [简体中文](../zh-CN/setup-client.md)

Client gồm hai tiến trình:

- **`gnl-service`** chạy dưới LocalSystem và làm mọi việc cần đặc quyền:
  adapter Wintun, WireGuard, route, nhận diện game.
- **`gnl-ui`** chạy dưới người dùng đang đăng nhập và không có chút đặc quyền
  nào: một icon ở tray, một panel mini và một cửa sổ đầy đủ.

UI chỉ yêu cầu service một trong bốn lệnh qua named pipe, ngoài ra không gì
khác.

Trang này dành cho người cài đặt và đóng gói client. Về cách dùng hằng ngày,
xem [hướng dẫn sử dụng](user-guide.md).

## Yêu cầu

- Windows 10 hoặc 11, bản 64-bit.
- Quyền Administrator để cài. Dùng hằng ngày thì không cần.
- Một contributor key và URL của control plane, do người vận hành cung cấp.

## 1. Lấy gói cài

Artifact CI `gamenolag-windows-amd64` chứa mọi thứ, đặt cạnh nhau:

| File | Là gì |
|---|---|
| `gnl-service.exe` | Service |
| `gnl-ui.exe` | Tray và cửa sổ, build với `-H=windowsgui` |
| `gnl-ui.exe.manifest` | **Phải luôn nằm cạnh `gnl-ui.exe`.** Thiếu nó thì menu ở tray không được tạo và icon bị mờ trên màn hình high-DPI |
| `install.ps1`, `uninstall.ps1` | Trình cài đặt và trình gỡ cài đặt |

Để tự build trên bất kỳ hệ điều hành nào:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags -H=windowsgui -o dist/gnl-ui.exe ./cmd/gnl-ui
cp deploy/windows/gnl-ui.exe.manifest deploy/windows/*.ps1 dist/
```

## 2. Cài đặt

Trong một cửa sổ PowerShell **chạy quyền administrator**, tại thư mục chứa gói
cài:

```powershell
Unblock-File .\*.ps1, .\*.exe
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://cp.example.com
```

`Unblock-File` gỡ dấu "tải từ Internet". Thiếu bước này, PowerShell từ chối
chạy script chưa ký được tải về. Installer từ chối URL control không phải
`https`, vì key được gửi đi dưới dạng bearer token.

Những gì nó làm:

| Ở đâu | Cái gì |
|---|---|
| `C:\Program Files\GameNoLag\` | Các binary và manifest. Người dùng đọc được, chỉ Administrators ghi được, vì nếu người dùng thay được một binary chạy dưới LocalSystem thì người đó có thể trở thành LocalSystem |
| `C:\ProgramData\GameNoLag\` | Trạng thái. Chỉ SYSTEM và Administrators đọc được, vì nó chứa private key của thiết bị và contributor key |
| Service `GameNoLag` | Được đăng ký để tự khởi động. Tunnel không bật cho tới khi UI yêu cầu connect |
| Start Menu, lúc đăng nhập | Một shortcut *GameNoLag*, và tray được đặt để tự chạy mỗi lần đăng nhập |

Installer **không** mở tray. Nó chạy với quyền nâng cao, nên bất cứ thứ gì nó
khởi chạy cũng sẽ chạy với quyền nâng cao và nằm trong session của
administrator. Hãy mở *GameNoLag* từ Start Menu, hoặc đăng xuất rồi đăng nhập
lại.

## 3. Cấu hình

`C:\ProgramData\GameNoLag\config.json` do installer ghi ra:

```json
{
  "control_url": "https://cp.example.com",
  "contributor_key": "GNL-XXXX-XXXX-XXXX",
  "games": [
    { "id": "pubg", "process_names": ["TslGame.exe"] }
  ]
}
```

- `games` là tuỳ chọn. Khi có, nó **thay thế** danh sách có sẵn.
- Các trường không xác định bị từ chối, để lỗi gõ nhầm báo lỗi rõ ràng thay vì
  lặng lẽ không làm gì.
- Khởi động lại service sau khi sửa: `Restart-Service GameNoLag`.

Các game có sẵn:

| id | Tiến trình |
|---|---|
| `pubg` | `TslGame.exe` |
| `valorant` | `VALORANT-Win64-Shipping.exe` |
| `lol` | `League of Legends.exe` |
| `csgo` | `cs2.exe` |
| `dota2` | `dota2.exe` |

Các file khác trong cùng thư mục:

| File | Là gì |
|---|---|
| `device.key` | WireGuard private key của máy này. Được tạo một lần và giữ lại, vì mỗi danh tính mới chiếm một slot thiết bị. File bị hỏng sẽ bị từ chối chứ không bị thay thế; nếu buộc phải xoá thì hãy xoá một cách có chủ đích |
| `service.log` | Được xoay sang `service.log.1` khi vượt quá 8 MB |

## 4. Connect lần đầu

1. Mở menu ở tray và chọn **Connect**.
2. Ở lần dùng đầu tiên, thiết bị tự kích hoạt, dùng hostname làm tên. Việc này
   chiếm một trong các slot thiết bị của key (mặc định là ba).
3. Client handshake với mọi relay, chọn relay nhanh nhất và ghim nó.
4. Khi một game trong danh sách khởi động, route của game đó được cài. Khi game
   thoát, route được gỡ.

Nếu kích hoạt thất bại với lỗi *slots full*, thông báo lỗi sẽ liệt kê các máy
đang giữ slot. Giải phóng một slot bằng cách gỡ cài đặt với `-Purge` trên một
máy bạn không dùng nữa, hoặc nhờ người vận hành.

## 5. Log

Cần quyền administrator để đọc `C:\ProgramData\GameNoLag\service.log`.

| Dòng | Ý nghĩa |
|---|---|
| `no relay answered` | Mạng chặn UDP tới các relay. Route bị gỡ và người chơi đi đường mạng bình thường |
| `relay X has not handshaken in over 2m30s` | Relay đó đã ngừng phản hồi. Client đang đo lại |
| `relay X: ... has no IPv4 address` | Relay đó bị bỏ qua. Client chỉ route IPv4 |
| `profile version N: ignoring "..."` | Một prefix sai định dạng trong profile đã bị bỏ |
| `... is not a usable key file` | `device.key` bị hỏng. Xoá nó sẽ tốn một slot thiết bị |

Để xem service chạy trực tiếp trong console (vẫn cần LocalSystem):

```
psexec -s -i "C:\Program Files\GameNoLag\gnl-service.exe"
```

## 6. Gỡ cài đặt

```powershell
.\uninstall.ps1          # keeps this machine's identity and its device slot
.\uninstall.ps1 -Purge   # also deletes it; reinstalling costs a new slot
```

Dừng service chính là thứ hoàn tác mọi tác động của nó lên mạng. Adapter biến
mất cùng tiến trình, và mọi route trỏ vào adapter cũng mất theo.

## Nếu Internet của người chơi bị hỏng

Dừng service (`Stop-Service GameNoLag`) hoặc khởi động lại máy. Cách nào cũng
đưa máy về nguyên trạng hoàn toàn:

- Adapter do service tạo ra, không dùng lại adapter có sẵn, và biến mất khi
  service thoát.
- Route chỉ được ghi vào bảng route đang hoạt động (active store), không bao
  giờ được lưu cố định, nên khởi động lại máy sẽ xoá sạch chúng.

Về cách client hoạt động bên trong, xem
[runbook client Windows](../windows-client-runbook.md) (tiếng Anh).
