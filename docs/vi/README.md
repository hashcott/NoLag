# Tài liệu GameNoLag

[English](../en/README.md) · **Tiếng Việt** · [简体中文](../zh-CN/README.md)

## Bắt đầu từ đây

| Nếu bạn… | Đọc |
|---|---|
| Là người chơi | [Hướng dẫn sử dụng](user-guide.md) |
| Cài đặt hoặc đóng gói client Windows | [Cài đặt client](setup-client.md) |
| Đóng góp một VPS làm relay | [Cài đặt relay](setup-relay.md) |
| Vận hành service cho người khác dùng | [Cài đặt control plane](setup-control-plane.md) |
| Muốn hiểu các phần ghép với nhau thế nào | [Kiến trúc](architecture.md) |
| Đóng góp code | [CONTRIBUTING](../../CONTRIBUTING.md) (tiếng Anh) |
| Báo cáo lỗ hổng bảo mật | [SECURITY](../../SECURITY.md) (tiếng Anh) |

## Dựng một hệ thống hoàn chỉnh, theo thứ tự

1. **Control plane.** Postgres, `gnl-control` đặt sau TLS, rồi tạo một
   contributor key.
   → [setup-control-plane.md](setup-control-plane.md)
2. **Relay đầu tiên.** Một VPS KVM gần máy chủ game: chạy `relay-v1.sh`, mở
   UDP 51820 ở phía nhà cung cấp, và xác minh từ bên ngoài bằng
   `gnl-relaycheck`.
   → [setup-relay.md](setup-relay.md)
3. **Game profile.** Chạy thử `gnl-profile`, xem lại kết quả, rồi chạy với
   `-publish`.
   → [setup-control-plane.md § 7](setup-control-plane.md#7-publish-a-game-profile)
4. **Client.** Cài gói Windows với contributor key, rồi Connect.
   → [setup-client.md](setup-client.md)

Relay được cung cấp cho người chơi khi nó ở trạng thái `up` và thời gian theo
dõi 48 giờ của nó đã qua.

## Tài liệu tham khảo chuyên sâu (tiếng Anh)

Các tài liệu này giải thích lý do đằng sau mỗi bước và mỗi phép kiểm tra chứng
minh điều gì.

- [Runbook client Windows](../windows-client-runbook.md)
- [Runbook P1: control plane và relay đầu tiên](../p1-runbook.md)
- [Runbook P0: đo chất lượng route](../p0-runbook.md)
