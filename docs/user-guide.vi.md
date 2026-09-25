# Hướng dẫn sử dụng GameNoLag

GameNoLag cho lưu lượng của game đi qua một relay đặt gần máy chủ game
(Singapore, Tokyo) thay vì đi theo đường mặc định của nhà mạng. Chỉ lưu lượng
của game đi qua relay; trình duyệt, Discord, tải file… vẫn đi đường mạng bình
thường.

Tài liệu này dành cho người chơi. Phần kỹ thuật nằm trong
[runbook của client Windows](windows-client-runbook.md).

## Cần có

- Windows 10 hoặc 11, bản 64-bit.
- Quyền quản trị (Administrator) để cài. Sau khi cài, dùng hằng ngày không cần
  quyền này.
- Một **contributor key** dạng `GNL-XXXX-XXXX-XXXX`, nhận từ người vận hành khi
  bạn đóng góp một VPS. Một key kích hoạt được tối đa **3 máy**.
- Địa chỉ **control plane** (bắt đầu bằng `https://`), cũng do người vận hành
  cung cấp.
- Gói cài `gamenolag-windows-amd64`, gồm `gnl-service.exe`, `gnl-ui.exe`,
  `gnl-ui.exe.manifest`, `install.ps1` và `uninstall.ps1`.

## Cài đặt

1. Giải nén gói cài vào một thư mục, ví dụ `Downloads\gamenolag`. Giữ nguyên
   5 file cạnh nhau.
2. Mở **PowerShell bằng quyền Administrator**: bấm Start, gõ `PowerShell`, bấm
   chuột phải rồi chọn *Run as administrator*.
3. Chuyển vào thư mục vừa giải nén rồi chạy:

   ```powershell
   cd $HOME\Downloads\gamenolag
   Unblock-File .\*.ps1, .\*.exe
   .\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://api.example.com
   ```

   `Unblock-File` gỡ dấu "tải từ Internet" mà Windows gắn vào file, nếu không
   PowerShell sẽ từ chối chạy script. Thay key và địa chỉ bằng giá trị của bạn.
   Installer không nhận địa chỉ `http://` vì key được gửi đi như mật khẩu.
4. Installer **không tự mở** GameNoLag. Mở nó từ Start Menu (*GameNoLag*), hoặc
   đăng xuất rồi đăng nhập lại. Từ lần sau, nó tự chạy mỗi khi bạn đăng nhập.

Installer đặt chương trình vào `C:\Program Files\GameNoLag` và dữ liệu vào
`C:\ProgramData\GameNoLag`. Chỉ quản trị viên đọc được thư mục dữ liệu, vì nó
chứa khoá riêng của máy.

## Sử dụng hằng ngày

### Icon ở khay hệ thống

GameNoLag nằm ở khay hệ thống, góc phải thanh taskbar. Nếu không thấy icon, bấm
mũi tên `^`.

| Icon | Nghĩa |
|---|---|
| Vòng xám rỗng | Chưa kết nối, hoặc service không chạy |
| Vòng xanh, có chấm đặc ở giữa | Đã kết nối. Khi có game đang chạy, lưu lượng game đi qua relay |
| Vòng đỏ, có thanh ngang cắt qua giữa | Đã kết nối nhưng có lỗi, ví dụ relay mất hoặc cài route thất bại |

Ba trạng thái khác nhau cả về hình dạng lẫn màu, nên người không phân biệt được
màu vẫn đọc được. Rê chuột lên icon để xem relay đang dùng và độ trễ.

**Bấm chuột phải** để mở menu:

| Mục | Làm gì |
|---|---|
| (dòng đầu tiên) | Trạng thái hiện tại. Chỉ để đọc |
| Open window | Mở cửa sổ đầy đủ |
| Connect | Kết nối. Chương trình đo mọi relay rồi chọn relay nhanh nhất, mất vài giây tới vài chục giây |
| Disconnect | Ngắt kết nối, trả mọi lưu lượng về đường mạng bình thường |
| Refresh game list | Tải lại danh sách dải địa chỉ của các game |
| Open log folder | Mở thư mục log. Nếu Windows báo *Access denied* thì đó là bình thường, xem mục Gặp sự cố |
| Exit | Đóng icon. **Không ngắt kết nối**: muốn ngắt thì bấm Disconnect trước |

### Cửa sổ mini và cửa sổ đầy đủ

**Bấm chuột trái** vào icon để mở panel mini ở góc màn hình, ngay trên
taskbar. Panel hiện độ trễ (rtt) kèm biểu đồ cột nhỏ, tỉ lệ mất gói (loss),
relay, game, một nút Connect/Disconnect, và lý do nếu đang không kết nối.

- **+** mở cửa sổ đầy đủ: biểu đồ độ trễ 3 phút, số route đang cài, và nhật ký
  sự kiện (kết nối, đổi relay, game bắt đầu/dừng, lỗi).
- **−** trong cửa sổ đầy đủ thu về panel mini.
- **×** ẩn panel mini. Bấm chuột trái vào icon lần nữa cũng ẩn.
- Phím **Tab** chuyển giữa các nút, **Enter** bấm nút đang chọn.

Đóng hay ẩn cửa sổ **không ngắt kết nối**. Biểu đồ và nhật ký chỉ giữ trong bộ
nhớ: chúng bắt đầu trống mỗi lần GameNoLag khởi động. Đoạn trống trên biểu đồ
nghĩa là lúc đó không đo được (chưa kết nối hoặc service không trả lời), chứ
không phải độ trễ bằng 0.

Panel mini luôn nằm trên các cửa sổ khác. Game chạy ở chế độ **borderless** hoặc
**windowed** sẽ thấy panel đè lên; game chạy **fullscreen độc quyền** thì không
thấy. Nếu không muốn panel che game, bấm **×** trước khi chơi.

### Khi chơi game

Bạn không cần làm gì thêm. Khi đã kết nối, GameNoLag tự nhận ra game đang chạy
và chỉ cài route cho game đó. Thoát game thì route được gỡ.

Các game được nhận ra sẵn:

| Game | Tiến trình |
|---|---|
| PUBG | `TslGame.exe` |
| Valorant | `VALORANT-Win64-Shipping.exe` |
| League of Legends | `League of Legends.exe` |
| Counter-Strike 2 | `cs2.exe` |
| Dota 2 | `dota2.exe` |

Muốn thêm game khác, nhờ người vận hành sửa danh sách (mục *Detecting a game*
trong runbook).

GameNoLag nhận ra game **chỉ bằng tên tiến trình**, giống cách Task Manager
liệt kê. Nó không mở, không đọc bộ nhớ và không can thiệp vào tiến trình game.
Nó chỉ làm việc ở tầng mạng bên dưới game.

Trong lúc bạn đang chơi, GameNoLag không tự đổi relay chỉ vì có relay khác
nhanh hơn, vì đổi relay giữa trận sẽ gây giật. Nó chỉ đổi giữa hai trận. Ngoại
lệ là relay đang dùng chết hẳn (không phản hồi 150 giây): khi đó nó chuyển
ngay sang relay khác. Nếu không còn relay nào phản hồi, nó trả game về đường
mạng bình thường và thử lại mỗi 30 giây.

## Gặp sự cố

| Hiện tượng | Nguyên nhân thường gặp | Cách xử lý |
|---|---|---|
| Trạng thái ghi *The GameNoLag service is not running* | Service bị dừng | Mở PowerShell Administrator, chạy `Start-Service GameNoLag` |
| Bấm Connect rồi báo *no relay answered* | Mạng đang dùng chặn UDP, thường gặp ở mạng công ty hoặc quán net | Thử mạng khác. Game vẫn chạy bình thường theo đường mạng thường |
| Báo hết slot thiết bị | Key đã kích hoạt đủ 3 máy | Gỡ GameNoLag kèm `-Purge` trên một máy không dùng nữa, hoặc nhờ người vận hành giải phóng slot |
| Icon đỏ có thanh ngang | Đã kết nối nhưng có lỗi | Mở cửa sổ đầy đủ đọc dòng lỗi. Thường tự hồi phục; nếu không, bấm Disconnect rồi Connect |
| *Open log folder* báo Access denied | Thư mục chứa khoá riêng nên chỉ quản trị viên đọc được | Bình thường. Log nằm ở `C:\ProgramData\GameNoLag\service.log`, mở bằng Notepad chạy quyền Administrator |
| Mất Internet hoàn toàn và nghi do GameNoLag | — | Dừng service (`Stop-Service GameNoLag`) hoặc khởi động lại máy. Mọi route của GameNoLag biến mất ngay khi service dừng |

Khi nhờ hỗ trợ, gửi kèm file `service.log` và mô tả lúc lỗi xảy ra bạn đang làm
gì.

## Gỡ cài đặt

Mở PowerShell Administrator trong thư mục chứa `uninstall.ps1`:

```powershell
.\uninstall.ps1          # gỡ chương trình, giữ danh tính máy
.\uninstall.ps1 -Purge   # gỡ luôn danh tính máy
```

Không có `-Purge`, cài lại sau này vẫn là "máy cũ" và không tốn thêm slot. Có
`-Purge`, lần cài sau được tính là một máy mới và tốn thêm một slot trên key.

## Quyền riêng tư

- Chỉ lưu lượng của game trong danh sách đi qua relay. Mọi thứ khác đi đường
  mạng bình thường.
- GameNoLag không đọc nội dung lưu lượng và không đọc gì từ tiến trình game.
- Khoá riêng của máy và contributor key nằm trong `C:\ProgramData\GameNoLag`,
  chỉ SYSTEM và quản trị viên đọc được. Máy chủ điều khiển chỉ lưu bản băm
  (hash) của contributor key, không lưu key gốc.
