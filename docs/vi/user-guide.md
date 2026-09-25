# Hướng dẫn sử dụng GameNoLag

[English](../en/user-guide.md) · **Tiếng Việt** · [简体中文](../zh-CN/user-guide.md)

GameNoLag cho lưu lượng game của bạn đi qua một relay (máy trung chuyển) đặt
gần máy chủ game (Singapore, Tokyo) thay vì đi theo route (đường đi) mặc định
của nhà mạng. Chỉ game đi qua relay. Trình duyệt, Discord, tải file và mọi thứ
khác vẫn đi đường mạng bình thường.

Tài liệu này dành cho người chơi. Về cài đặt và đóng gói, xem
[Cài đặt client Windows](setup-client.md).

## Cần có

- Windows 10 hoặc 11, bản 64-bit.
- Quyền quản trị (Administrator) để cài. Sau đó, dùng hằng ngày không cần quyền
  này.
- Một **contributor key** (`GNL-XXXX-XXXX-XXXX-XXXX`) do người vận hành cấp khi bạn
  đóng góp một VPS. Một key kích hoạt được tối đa **3 PC**.
- **Địa chỉ control plane** (máy chủ điều khiển, bắt đầu bằng `https://`), cũng
  do người vận hành cung cấp.

## Cài đặt

1. Tải **`GameNoLag-Setup-<version>.exe`** từ
   [bản release mới nhất](https://github.com/hashcott/NoLag/releases/latest).
2. Bấm đúp vào file. Windows hỏi quyền administrator; hãy cho phép.
   Chừng nào trình cài đặt chưa được ký số (code-signed), Windows SmartScreen có
   thể báo *Windows protected your PC* (Windows đã bảo vệ PC của bạn). Bấm
   **More info → Run anyway** (Thông tin thêm → Vẫn chạy), nhưng chỉ với file
   bạn tải từ trang release ở trên.
3. Đồng ý với giấy phép, rồi nhập **contributor key** và **địa chỉ control
   plane**. Ô địa chỉ có thể đã được điền sẵn.
4. Bấm hoàn tất khi ô **Start GameNoLag** đang được đánh dấu. Icon xuất hiện ở
   vùng thông báo, và từ đó trở đi nó tự chạy mỗi khi bạn đăng nhập.

Nâng cấp cũng làm y như vậy: chạy bản setup mới hơn đè lên bản cũ. PC của bạn
giữ nguyên danh tính, nên không tốn thêm slot thiết bị.

## Sử dụng hằng ngày

### Icon ở tray

GameNoLag nằm ở tray (khay hệ thống), góc phải thanh taskbar. Nếu không thấy
icon, bấm mũi tên `^`.

| Icon | Nghĩa |
|---|---|
| Vòng xám rỗng | Chưa kết nối, hoặc service không chạy |
| Vòng xanh, có chấm đặc ở giữa | Đã kết nối. Khi có game đang chạy, lưu lượng game đi qua relay |
| Vòng đỏ, có thanh ngang cắt qua | Đã kết nối nhưng có vấn đề, ví dụ mất relay hoặc cài route thất bại |

Ba trạng thái khác nhau cả về hình dạng lẫn màu sắc. Rê chuột lên icon để xem
relay đang dùng và độ trễ.

**Bấm chuột phải** để mở menu:

| Mục | Làm gì |
|---|---|
| (dòng đầu tiên) | Trạng thái hiện tại. Chỉ để đọc |
| Open window | Mở cửa sổ đầy đủ |
| Connect | Đo mọi relay rồi chọn relay nhanh nhất. Mất từ vài giây tới vài chục giây |
| Disconnect | Trả mọi lưu lượng về đường mạng bình thường |
| Refresh game list | Tải lại các dải địa chỉ của game |
| Open log folder | Mở thư mục log. Báo *Access denied* là bình thường; xem mục Gặp sự cố |
| Exit | Đóng icon. **Không ngắt kết nối**; muốn ngắt thì bấm Disconnect trước |

### Panel mini và cửa sổ đầy đủ

**Bấm chuột trái** vào icon để mở panel mini ngay trên taskbar. Panel hiện độ
trễ (rtt) kèm biểu đồ cột nhỏ, tỉ lệ mất gói, relay, game, một nút
Connect/Disconnect, và lý do khi bạn chưa kết nối.

| Nút | Làm gì |
|---|---|
| **+** | Mở cửa sổ đầy đủ: biểu đồ độ trễ 3 phút, các route đang hoạt động, và nhật ký sự kiện (kết nối, đổi relay, game bắt đầu và dừng, lỗi) |
| **−** | Trong cửa sổ đầy đủ, thu về panel mini |
| **×** | Ẩn panel mini. Bấm chuột trái vào icon lần nữa cũng có tác dụng tương tự |
| **Tab** / **Enter** | Chuyển giữa các nút / bấm nút đang chọn |

Đóng hay ẩn cửa sổ **không bao giờ ngắt kết nối**. Biểu đồ và nhật ký chỉ nằm
trong bộ nhớ và bắt đầu trống mỗi lần GameNoLag khởi động. Đoạn trống trên biểu
đồ nghĩa là lúc đó không có phép đo nào (chưa kết nối, hoặc service không trả
lời), chứ không phải độ trễ bằng 0.

Panel mini luôn nằm trên các cửa sổ khác. Nó hiện đè lên game chạy ở chế độ
**borderless** hoặc **windowed**, nhưng không hiện trên game chạy **exclusive
fullscreen** (toàn màn hình độc quyền). Nếu không muốn panel che game, bấm
**×** trước khi chơi.

### Khi chơi game

Bạn không cần làm gì cả. Khi đã kết nối, GameNoLag nhận ra một game trong danh
sách vừa khởi động và chỉ route riêng game đó. Khi game thoát, route được gỡ.

| Game | Tiến trình |
|---|---|
| PUBG | `TslGame.exe` |
| Valorant | `VALORANT-Win64-Shipping.exe` |
| League of Legends | `League of Legends.exe` |
| Counter-Strike 2 | `cs2.exe` |
| Dota 2 | `dota2.exe` |

GameNoLag nhận ra game **chỉ bằng tên tiến trình**, giống cách Task Manager
liệt kê. Nó không bao giờ mở, đọc hay sửa đổi tiến trình game; nó chỉ làm việc
ở tầng mạng bên dưới.

Nó sẽ không đổi relay giữa trận chỉ vì có relay khác nhanh hơn, vì như vậy sẽ
gây giật. Nó chỉ đổi giữa các trận. Ngoại lệ là khi relay chết hẳn (không phản
hồi trong 150 giây): GameNoLag chuyển ngay sang relay khác. Nếu không còn relay
nào phản hồi, game của bạn quay về đường mạng bình thường, và GameNoLag thử lại
mỗi 30 giây.

## Gặp sự cố

| Hiện tượng | Nguyên nhân thường gặp | Cách xử lý |
|---|---|---|
| *The GameNoLag service is not running* | Service đã dừng | Mở PowerShell Administrator, chạy `Start-Service GameNoLag` |
| *no relay answered* sau khi bấm Connect | Mạng của bạn chặn UDP (thường gặp ở mạng công ty hoặc quán cà phê) | Thử mạng khác. Game vẫn chạy bình thường theo đường mạng thường |
| *no device slots left* (hết slot thiết bị) | Key đã dùng cho đủ 3 PC | Xoá danh tính trên một PC không dùng nữa (xem mục Gỡ cài đặt), hoặc nhờ người vận hành |
| Icon đỏ có thanh ngang | Đã kết nối nhưng có lỗi | Đọc dòng lỗi trong cửa sổ đầy đủ. Thường sẽ tự hồi phục; nếu không, bấm Disconnect rồi Connect |
| *Open log folder* báo Access denied | Thư mục chứa private key (khoá riêng) của bạn, nên chỉ quản trị viên đọc được | Bình thường. Log nằm ở `C:\ProgramData\GameNoLag\service.log`; mở bằng Notepad chạy quyền Administrator |
| Mất Internet hoàn toàn và nghi do GameNoLag | — | `Stop-Service GameNoLag`, hoặc khởi động lại máy. Mọi route của GameNoLag biến mất ngay khi service dừng |

Khi nhờ hỗ trợ, hãy gửi kèm file `service.log` và mô tả bạn đang làm gì lúc lỗi
xảy ra.

## Gỡ cài đặt

**Settings → Apps → Installed apps → GameNoLag → Uninstall** (Cài đặt → Ứng
dụng → Ứng dụng đã cài → GameNoLag → Gỡ cài đặt). Thao tác này gỡ chương trình
và giữ lại danh tính của PC này, nên lần cài lại sau không tốn thêm slot thiết
bị.

Muốn giải phóng luôn slot, chẳng hạn vì bạn định cho người khác chiếc PC này,
hãy xoá cả danh tính. Trong một PowerShell Administrator:

```powershell
Remove-Item -Recurse -Force "$env:ProgramData\GameNoLag"
```

Khi đó, lần cài tiếp theo được tính là một PC mới. Người vận hành cũng có thể
giải phóng slot giúp bạn.

## Quyền riêng tư

- Chỉ lưu lượng của các game trong danh sách đi qua relay. Mọi thứ khác vẫn đi
  đường mạng bình thường.
- GameNoLag không đọc nội dung lưu lượng của bạn, cũng không đọc bất cứ thứ gì
  bên trong tiến trình game.
- Private key của PC và contributor key của bạn nằm trong
  `C:\ProgramData\GameNoLag`, chỉ SYSTEM và quản trị viên đọc được. Control
  plane chỉ lưu bản hash (bản băm) của contributor key.
