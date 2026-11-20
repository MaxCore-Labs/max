---
name: ingest-source
description: Lưu nguồn thô vào sources/ và chưng cất bài viết có trích nguồn đầy đủ vào wiki/.
---
# Thu nạp và chưng cất nguồn thông tin

1. Lưu bản chụp nội dung gốc vào `sources/YYYY-MM-DD-<slug>.md` kèm dòng đầu nêu rõ URL và thời điểm thu thập.
2. Tổng hợp các ý đã kiểm chứng vào `wiki/<chủ-đề>.md` với liên kết ngược tới tệp trong `sources/`.
3. Nếu nguồn mới mâu thuẫn với bài có sẵn trong `wiki/`, bổ sung mục `## Contradiction` nêu cả hai nguồn thay vì ghi đè.
