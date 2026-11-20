---
name: make-routine
description: Soạn đề xuất công việc định kỳ qua routine_propose với lịch cron và nguồn gốc rõ ràng.
---
# Đề xuất lịch định kỳ (Routine)

1. Xác định rõ chu kỳ cron 5 trường hợp lý (ưu tiên lịch thưa để tiết kiệm hạn mức gói).
2. Nếu công việc định kỳ có đọc email, biểu mẫu hoặc dữ liệu từ người ngoài gửi vào, bắt buộc khai `origin: inbound`.
3. Gọi `routine_propose` kèm hướng dẫn thực hiện ngắn gọn; đề xuất sẽ ở trạng thái tắt cho tới khi chủ nhân duyệt.
