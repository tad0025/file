# TeleStream &bull; Encrypted Video Storage & Multi-Quality Streaming (Go Monolith)

Hệ thống Monolith viết bằng **Go** kết hợp giữa lưu trữ video mã hóa trên kênh Telegram qua giao thức MTProto và streaming video đa độ phân giải (`original`, `1080p`, `720p`), được thiết kế để chạy mượt mà trên môi trường container cực kỳ giới hạn tài nguyên (**256MB RAM, 0.25 vCPU** trên Back4App).

---

## 1. Kiến trúc Tổng quan

Hệ thống được chia thành 2 module độc lập:

1. **CLI Upload Tool (`cmd/uploader`) - Chạy tại máy cá nhân (Local)**:
   - Dùng **ffprobe** tự động dò độ phân giải video gốc.
   - Dùng **FFmpeg** encode thành các phiên bản: `original` (giữ nguyên), `1080p` (nếu gốc $\ge 1080p$), `720p` (nếu gốc $\ge 720p$).
   - Tự động chia nhỏ các file $> 2\text{GB}$ thành các part $\le 2\text{GB}$ (ngưỡng an toàn cho Telegram MTProto).
   - Mã hóa từng part bằng thuật toán **AES-256-CTR** với vector khởi tạo `iv` ngẫu nhiên (16 bytes hex) riêng biệt cho mỗi part.
   - Đẩy các file mã hóa `.enc` lên Telegram Channel qua tài khoản MTProto bằng chuỗi `TG_SESSION_STRING`.
   - Ghi dữ liệu phân cấp vào MySQL: `videos` $\rightarrow$ `video_qualities` $\rightarrow$ `video_parts`.
   - Tự động dọn dẹp các file `.enc` và file tạm sau khi hoàn tất.

2. **Streaming Server (`cmd/server`) - Deploy trên Back4App (256MB RAM)**:
   - Bảo vệ toàn bộ giao diện và API bằng **`PASSWORD`** (cấp phát token JWT lưu trong cookie `auth_token` HTTP-only).
   - **Giao diện Web Player (Dark Theme)**: Sử dụng TailwindCSS + Video.js tích hợp menu chọn độ phân giải (`Gốc`, `1080p`, `720p`). Khi chuyển độ phân giải, player tự động giữ nguyên mốc thời gian đang phát (`currentTime`).
   - **Zero-disk & Low-RAM Streaming Engine**:
     - Tiếp nhận HTTP Range Request (`206 Partial Content`) tại `/api/stream/{quality_id}`.
     - Lấy từng chunk nhỏ (512KB) qua socket MTProto (`upload.getFile`) trực tiếp từ Telegram.
     - Giải mã **AES-256-CTR on-the-fly** trực tiếp trên RAM: Tua trực tiếp tới byte bất kỳ bằng cách dịch counter $\text{IV} + \lfloor \text{local\_offset} / 16 \rfloor$.
     - Pipe dữ liệu video thô trực tiếp ra `http.ResponseWriter`. Client nhận video phát ngay mà không cần giải mã trên trình duyệt.
     - RAM server luôn ổn định dưới **40MB RAM** nhờ cơ chế tái sử dụng buffer (`sync.Pool`).

---

## 2. Cấu trúc Thư mục Dự án

```text
d:\Tool\file\
├── cmd\
│   ├── server\
│   │   └── main.go              # Entrypoint Server Streaming & Web UI (Back4App)
│   └── uploader\
│       └── main.go              # Entrypoint CLI Uploader Local (FFmpeg, AES-CTR, Telegram)
├── internal\
│   ├── config\
│   │   └── config.go            # Fail-Fast Env Loader (Zero-Fallback)
│   ├── database\
│   │   ├── db.go                # MySQL Connection Pool (max 5 conns) & Auto-Migration
│   │   └── models.go            # Structs: Video, VideoQuality, VideoPart
│   ├── crypto\
│   │   └── aes_ctr.go           # AES-256-CTR Seekable Cipher & IV Generator
│   ├── telegram\
│   │   ├── session.go           # Telethon v1 StringSession Unpacker cho gotd/td
│   │   ├── client.go            # Quản lý kết nối MTProto DC 5 & Channel Peer
│   │   ├── uploader.go          # UploadSaveBigFilePart (512KB chunks)
│   │   └── downloader.go        # UploadGetFile chunked reader căn lề 4KB MTProto
│   ├── ffmpeg\
│   │   └── transcoder.go        # FFmpeg Transcoding Pipeline (1080p, 720p)
│   ├── auth\
│   │   └── auth.go              # Xác thực PASSWORD & JWT Cookie Middleware
│   └── handler\
│       ├── web.go               # Web Page Handlers (/login, /, /watch/:id)
│       ├── api.go               # API Endpoints (/api/login, /api/videos, /health)
│       └── stream.go            # HTTP Range Request 206, Virtual Concatenation
├── web\
│   └── templates\
│       ├── base.html            # Dark Theme Layout (TailwindCSS + Video.js CDN)
│       ├── login.html           # Màn hình đăng nhập PASSWORD kính mờ
│       ├── index.html           # Danh sách video, badge chất lượng
│       └── watch.html           # Player Video.js có dropdown đổi độ phân giải giữ currentTime
├── Dockerfile                   # Multi-stage build tối ưu cho Back4App (<30MB)
├── .dockerignore
├── .gitignore                   # Đã bảo vệ .env và file nhị phân
├── .env                         # Cấu hình biến môi trường thực tế
└── README.md                    # Tài liệu hướng dẫn sử dụng
```

---

## 3. Thiết kế Cơ sở Dữ liệu MySQL

Dữ liệu được lưu trữ trên MySQL (`videostreamtele_db`) theo mô hình 3 cấp (tự động chạy migration khi server khởi động):

```sql
-- 1. Bảng Video tổng
CREATE TABLE IF NOT EXISTS videos (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2. Bảng các phiên bản độ phân giải (original, 1080p, 720p)
CREATE TABLE IF NOT EXISTS video_qualities (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    video_id BIGINT NOT NULL,
    quality VARCHAR(20) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    mime_type VARCHAR(100) DEFAULT 'video/mp4',
    total_size BIGINT NOT NULL,
    is_multi_part BOOLEAN DEFAULT FALSE,
    FOREIGN KEY (video_id) REFERENCES videos(id) ON DELETE CASCADE,
    UNIQUE KEY uq_video_quality (video_id, quality)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 3. Bảng các Part file trên Telegram
CREATE TABLE IF NOT EXISTS video_parts (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    quality_id BIGINT NOT NULL,
    part_order INT NOT NULL,
    tg_chat_id BIGINT NOT NULL,
    tg_message_id BIGINT NOT NULL,
    part_size BIGINT NOT NULL,
    start_byte BIGINT NOT NULL,
    end_byte BIGINT NOT NULL,
    iv VARCHAR(32) NOT NULL, -- Vector IV 16-byte hex riêng biệt từng part
    FOREIGN KEY (quality_id) REFERENCES video_qualities(id) ON DELETE CASCADE,
    INDEX idx_quality_parts (quality_id, part_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

---

## 4. Cấu hình Biến Môi Trường (`.env`)

Nguyên tắc **Fail-Fast (Zero-fallback)**: Thiếu bất kỳ biến nào sau đây, server sẽ dừng ngay lập tức:

```env
# Telegram MTProto Credentials (lấy từ my.telegram.org)
TG_API_ID=your_telegram_api_id
TG_API_HASH=your_telegram_api_hash
TG_SESSION_STRING=your_telethon_string_session
TG_CHANNEL_ID=-100xxxxxxxxxx

# Bảo mật & Mã hóa
JWT_SECRET=your_super_secret_jwt_key_here
AES_SECRET_KEY=your_32_bytes_aes_key_here_____ # Chuẩn đúng 32 bytes
PASSWORD=your_secure_web_password             # Mật khẩu mở khóa Web

# Cấu hình Web Server
PORT=8080
CHUNK_SIZE=512                                # Chunk 512KB tối ưu MTProto

# Database MySQL Aiven
MYSQL_URL=mysql://user:password@host:port/dbname?ssl-mode=REQUIRED
```

---

## 5. Danh sách Endpoint Hệ thống

| Method | Endpoint | Quyền truy cập | Mô tả |
| :--- | :--- | :--- | :--- |
| `GET` | `/health` | Công khai | Kiểm tra kết nối MySQL, Telegram và trạng thái Container |
| `GET` | `/login` | Công khai | Trang đăng nhập mật khẩu |
| `POST` | `/api/login` | Công khai | Xác thực mật khẩu, cấp cookie JWT `auth_token` |
| `POST` | `/api/logout` | Đã đăng nhập | Xóa cookie đăng nhập |
| `GET` | `/` | Đã đăng nhập | Thư viện video (bảng card, badge chất lượng) |
| `GET` | `/watch/{id}` | Đã đăng nhập | Giao diện phát Video.js kèm dropdown chọn độ phân giải |
| `GET` | `/api/videos` | Đã đăng nhập | Trả về JSON danh sách video kèm các bản chất lượng |
| `GET` | `/api/videos/{id}` | Đã đăng nhập | Trả về JSON chi tiết video và các part |
| `GET` | `/api/stream/{quality_id}` | Đã đăng nhập | Stream HTTP 206 Partial Content video thô đã giải mã |

---

## 6. Hướng dẫn Sử dụng Thực tế

### A. Tải và Mã hóa Video bằng CLI Uploader (Chạy trên máy tính)

Đảm bảo máy tính đã cài đặt **FFmpeg** (`ffmpeg` có trong PATH):

```bash
# 1. Chạy trực tiếp qua Go
go run ./cmd/uploader -file "D:\Videos\video.mp4" -title "Video Kỷ Niệm"

# 2. Hoặc biên dịch ra file thực thi chạy nhanh hơn
go build -o bin/uploader.exe ./cmd/uploader
.\bin\uploader.exe -file "D:\Videos\video.mp4" -title "Video Kỷ Niệm"

# 3. Tùy chọn giữ lại các file encode 1080p/720p sau khi upload:
.\bin\uploader.exe -file "video.mp4" -keep-transcodes
```

### B. Chạy Server cục bộ trên máy

```bash
# 1. Chạy trực tiếp qua Go
go run ./cmd/server

# 2. Hoặc biên dịch file chạy
go build -o bin/server.exe ./cmd/server
.\bin\server.exe
```
Truy cập: `http://localhost:8080` &bull; Nhập mật khẩu đã cấu hình trong biến `PASSWORD` để vào xem.

### C. Triển khai lên Back4App Containers (256MB RAM / 0.25 vCPU)

1. Đẩy mã nguồn lên Git repository (GitHub / GitLab).
2. Trên Dashboard **Back4App Containers**:
   - Chọn repository và branch `main`.
   - **Port**: Điền `8080`.
   - **Environment Variables**: Nhập đầy đủ 10 biến từ mục 4 (`TG_API_ID`, `TG_API_HASH`, `TG_SESSION_STRING`, `TG_CHANNEL_ID`, `JWT_SECRET`, `AES_SECRET_KEY`, `PASSWORD`, `PORT`, `CHUNK_SIZE`, `MYSQL_URL`).
3. Back4App tự động thực hiện **Multi-stage Docker build** từ `Dockerfile`:
   - Dung lượng container image: **< 30MB**.
   - Bộ nhớ RAM thực tế khi chạy: **~15MB - 35MB**, hoàn toàn không chạm trần 256MB.