# Sổ tay vận hành Max (Phase 1 Runbook)

Tài liệu này hướng dẫn triển khai, đăng nhập `agy` lần đầu, sao lưu, khôi phục và xử lý sự cố vận hành cho Max theo kiến trúc một container (Cách A) hoặc `systemd` (Cách B).

---

## 1. Triển khai lần đầu & Đăng nhập `agy` trong `HOME` riêng

Mỗi engine CLI chạy dưới thư mục `HOME` cô lập quyền `0700` tại `<MAX_DATA_DIR>/agent-home/agy` để bảo vệ phiên đăng nhập.

### 1.1. Với Docker / Podman Compose (Cách A — Mặc định)

1. Tạo file `.env` từ mẫu và đặt mật khẩu khởi tạo (>= 12 ký tự):
   ```sh
   cp .env.example .env
   mkdir -p data/agent-home/agy vault opt-agy
   chmod 700 data data/agent-home data/agent-home/agy
   ```
   *(Đặt binary `agy` đã cài trên máy chủ vào `./opt-agy/agy` hoặc trỏ `AGY_HOST_DIR` tới thư mục chứa binary `agy` Linux.)*

2. Build image một container:
   ```sh
   docker compose build
   ```

3. Đăng nhập `agy` lần đầu trong `HOME` riêng (`/data/agent-home/agy`):
   ```sh
   docker compose run --rm --entrypoint agy max auth login
   ```
   Xác nhận trạng thái và ghi nhận SHA-256 của binary `agy`:
   ```sh
   docker compose run --rm max engine confirm agy
   docker compose run --rm max doctor
   ```

4. Khởi chạy daemon (chỉ publish `127.0.0.1:8787`, đi ra ngoài qua Tailscale Serve):
   ```sh
   docker compose up -d
   ```
   *(Sau khi đăng nhập UI lần đầu thành công, bạn có thể xóa `MAX_ADMIN_PASSWORD` khỏi `.env` vì mật khẩu đã được băm Argon2id trong SQLite.)*

### 1.2. Với `systemd` không Docker (Cách B)

```sh
sudo useradd --system --home-dir /var/lib/max --create-home --shell /usr/sbin/nologin max
sudo install -d -m 0700 -o max -g max /var/lib/max /var/lib/max/agent-home/agy /var/lib/max-vault
sudo -u max HOME=/var/lib/max/agent-home/agy agy auth login
sudo -u max MAX_DATA_DIR=/var/lib/max MAX_VAULT_DIR=/var/lib/max-vault max engine confirm agy
sudo -u max MAX_DATA_DIR=/var/lib/max MAX_VAULT_DIR=/var/lib/max-vault max doctor
sudo cp deploy/max.service /etc/systemd/system/max.service
sudo systemctl daemon-reload && sudo systemctl enable --now max
```

---

## 2. Kiểm tra nghiệm thu thủ công (Manual Smoke & Mid-Run Restart)

1. Build và khởi chạy dịch vụ:
   ```sh
   docker compose build
   docker compose run --rm --entrypoint agy max
   docker compose up -d
   docker compose ps
   ```
2. Mở `http://127.0.0.1:8787`, đăng nhập bằng mật khẩu chủ nhân và gửi một tin nhắn thật cho `chief-of-staff`.
3. Ngay khi giao diện bắt đầu stream `run.text` (run đang ở trạng thái `running`), khởi động lại container giữa chừng:
   ```sh
   docker compose restart max
   ```
4. Quan sát kết quả sau khi `max` lên lại:
   - Run đang dở được chuyển sang `interrupted`, `attempts` của wakeup không bị phạt lỗi crash, và wakeup tự động được nhận lại.
   - Run mới nối tiếp phiên ACP của conversation kèm ghi chú lượt trước bị ngắt và hoàn tất trả lời mà không lặp tác động.

---

## 3. Đổi mật khẩu chủ nhân

Đổi mật khẩu trực tiếp qua CLI (đọc từ stdin để không lộ trong lịch sử shell) — mọi phiên đăng nhập cũ sẽ tự động bị thu hồi:

```sh
# Docker Compose:
docker compose exec -T max max admin set-password

# Systemd / Host:
MAX_DATA_DIR=/var/lib/max max admin set-password
```

---

## 4. Sao lưu trực tuyến (Online Backup)

Script `scripts/backup.sh` dùng `VACUUM INTO` để chụp ảnh nhất quán `max.sqlite3` ngay cả khi `max serve` đang chạy ở chế độ WAL, đồng thời nén thư mục `agent-home` thành gói `.tar.gz` quyền `0600`:

```sh
MAX_DATA_DIR=./data bash scripts/backup.sh ./backups
```

Đối với vault Markdown (`./vault`), bật `MAX_VAULT_REMOTE=<private-git-url>` để Max tự động commit và push định kỳ, hoặc đồng bộ thêm thư mục `./vault` ra kho lưu trữ ngoài máy.

---

## 5. Khôi phục trên thư mục sạch (Clean Restore)

1. Dừng tiến trình `max` để giải phóng kết nối SQLite:
   ```sh
   docker compose down
   # hoặc: sudo systemctl stop max
   ```
2. Chạy `scripts/restore.sh` vào thư mục dữ liệu sạch (thêm `--force` nếu cần ghi đè thư mục cũ):
   ```sh
   rm -rf ./data
   MAX_DATA_DIR=./data bash scripts/restore.sh ./backups/max-backup-YYYYMMDDTHHMMSSZ.tar.gz
   ```
3. Kiểm tra toàn vẹn trước khi mở lại dịch vụ:
   ```sh
   docker compose run --rm max doctor
   docker compose up -d
   ```

---

## 6. Nâng cấp phiên bản (`max` hoặc `agy`)

### 6.1. Nâng cấp `max`
1. Sao lưu trước khi nâng cấp: `bash scripts/backup.sh ./backups`.
2. Build image / binary mới và khởi động lại (`docker compose up -d --build`).
3. Migration SQLite nhúng trong binary sẽ tự chạy khi khởi động; các run đang chạy nhận `SIGTERM`, chuyển sang `interrupted` và tự resume sau khi bản mới lên.

### 6.2. Khi binary `agy` thay đổi (Tamper / Auto-Update Guard)
Max đối chiếu mã băm SHA-256 của binary `agy` lúc khởi động và mỗi giờ với bảng `engine_binaries`. Nếu `agy` tự cập nhật hoặc đổi binary:
1. Max tự động chuyển engine `agy` sang `unavailable`, tạo thông báo khẩn trong `/inbox` và tạm xếp hàng các wakeup mới.
2. Kiểm tra phiên bản `agy` mới (từ Phase 2 trở đi: chạy cổng `max eval --engine agy`), sau đó xác nhận mã băm mới:
   ```sh
   docker compose exec max max engine confirm agy
   ```
   Ở chu kỳ kiểm tra kế tiếp (hoặc khi restart), engine `agy` sẽ mở lại và xử lý hàng đợi.

---

## 7. Xử lý khi `agy` mất đăng nhập

Triệu chứng: trang `/system` báo trạng thái `agy` là **Chưa đăng nhập** (`loggedIn: false`), đèn trạng thái chuyển cảnh báo và các việc mới đứng chờ trong hàng đợi mà không bị đánh rớt.

Cách khắc phục:
1. Đăng nhập lại trong đúng `HOME` riêng của `agy`:
   ```sh
   docker compose run --rm --entrypoint agy max auth login
   # hoặc trên host systemd:
   sudo -u max HOME=/var/lib/max/agent-home/agy agy auth login
   ```
2. Kiểm tra lại bằng `max doctor`:
   ```sh
   docker compose run --rm max doctor
   ```
3. Không cần xóa hàng đợi hay tạo lại việc: vòng kiểm tra sức khỏe của `max serve` sẽ tự động phát hiện `LoggedIn=true` và rút tiếp toàn bộ wakeup đang chờ.
