package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"videostream/internal/config"
	"videostream/internal/crypto"
	"videostream/internal/database"
	"videostream/internal/ffmpeg"
	"videostream/internal/telegram"
)

func main() {
	filePath := flag.String("file", "", "Đường dẫn tới file video gốc (Bắt buộc)")
	title := flag.String("title", "", "Tiêu đề hiển thị cho video (Tùy chọn)")
	keepTranscodes := flag.Bool("keep-transcodes", false, "Giữ lại các file video sau khi transcode (mặc định xóa)")
	flag.Parse()

	if *filePath == "" {
		fmt.Println("Lỗi: Thiếu tham số -file")
		fmt.Println("Sử dụng: go run ./cmd/uploader -file \"path/to/video.mp4\" [-title \"Tiêu đề\"]")
		os.Exit(1)
	}

	absPath, err := filepath.Abs(*filePath)
	if err != nil {
		log.Fatalf("Lỗi đường dẫn file: %v", err)
	}

	stat, err := os.Stat(absPath)
	if err != nil {
		log.Fatalf("File không tồn tại: %v", err)
	}

	videoTitle := *title
	if videoTitle == "" {
		videoTitle = filepath.Base(absPath)
	}

	log.Println("==================================================")
	log.Println("TeleStream CLI Video Uploader & Transcoder")
	log.Println("==================================================")
	log.Printf("File gốc: %s (%d bytes)", absPath, stat.Size())
	log.Printf("Tiêu đề:  %s", videoTitle)

	// 1. Nạp cấu hình & kiểm tra biến môi trường
	cfg := config.MustLoad()

	// 2. Kiểm tra công cụ FFmpeg
	if err := ffmpeg.CheckFFmpeg(); err != nil {
		log.Fatalf("FATAL: %v. Hãy cài đặt FFmpeg và thêm vào PATH.", err)
	}

	// 3. Kết nối MySQL
	log.Println("[MySQL] Connecting to database...")
	db, err := database.Connect(cfg.MySQLURL)
	if err != nil {
		log.Fatalf("FATAL: Kết nối MySQL thất bại: %v", err)
	}
	defer db.Close()

	// 4. Khởi tạo thư mục tạm để transcode & mã hóa
	tempDir, err := os.MkdirTemp("", "telestream_upload_*")
	if err != nil {
		log.Fatalf("Tạo thư mục tạm thất bại: %v", err)
	}
	defer func() {
		if !*keepTranscodes {
			_ = os.RemoveAll(tempDir)
		}
	}()

	// 5. Transcode 3 phiên bản độ phân giải
	log.Println("[FFmpeg] Transcoding video to available resolutions (original, 1080p, 720p)...")
	transcodedFiles, err := ffmpeg.TranscodeAll(absPath, tempDir)
	if err != nil {
		log.Fatalf("Transcode video thất bại: %v", err)
	}

	// 6. Khởi tạo Telegram Client
	log.Println("[Telegram] Initializing MTProto client...")
	tgClient, err := telegram.NewClient(cfg)
	if err != nil {
		log.Fatalf("Khởi tạo Telegram client thất bại: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Chạy quy trình upload bên trong Run loop của gotd
	err = tgClient.Run(ctx, func(uploadCtx context.Context) error {
		// Đợi client định vị channel peer
		time.Sleep(2 * time.Second)

		// Tạo bản ghi Video trong MySQL
		videoID, err := db.CreateVideo(videoTitle)
		if err != nil {
			return fmt.Errorf("create video in db: %w", err)
		}
		log.Printf("[Database] Created Video record ID: %d", videoID)

		// Thứ tự ưu tiên chất lượng
		qualityOrder := []string{"original", "1080p", "720p"}

		for _, q := range qualityOrder {
			qFilePath, exists := transcodedFiles[q]
			if !exists {
				continue
			}

			qStat, err := os.Stat(qFilePath)
			if err != nil {
				return fmt.Errorf("stat file %s: %w", qFilePath, err)
			}
			qTotalSize := qStat.Size()

			const maxPartBytes = int64(2000000000) // 2GB ngưỡng an toàn cho Telegram
			isMultiPart := qTotalSize > maxPartBytes

			vq := &database.VideoQuality{
				VideoID:     videoID,
				Quality:     q,
				FileName:    filepath.Base(qFilePath),
				MimeType:    "video/mp4",
				TotalSize:   qTotalSize,
				IsMultiPart: isMultiPart,
			}

			qualityID, err := db.CreateQuality(vq)
			if err != nil {
				return fmt.Errorf("create quality in db: %w", err)
			}
			log.Printf("[Database] Created Quality '%s' (ID: %d, Size: %d bytes)", q, qualityID, qTotalSize)

			// Mở file để chia part và mã hóa
			file, err := os.Open(qFilePath)
			if err != nil {
				return fmt.Errorf("open %s: %w", qFilePath, err)
			}

			var offset int64 = 0
			partOrder := 1

			for offset < qTotalSize {
				partBytes := maxPartBytes
				if remaining := qTotalSize - offset; remaining < partBytes {
					partBytes = remaining
				}

				startByte := offset
				endByte := offset + partBytes - 1

				// Sinh IV riêng cho từng part
				ivBytes, ivHex, err := crypto.GenerateIV()
				if err != nil {
					file.Close()
					return fmt.Errorf("generate IV: %w", err)
				}

				// Tạo file tạm mã hóa cho part này
				encPartPath := filepath.Join(tempDir, fmt.Sprintf("%s_part%d.enc", q, partOrder))
				encFile, err := os.Create(encPartPath)
				if err != nil {
					file.Close()
					return fmt.Errorf("create enc file: %w", err)
				}

				log.Printf("[%s Part %d] Encrypting bytes %d - %d with AES-256-CTR...", q, partOrder, startByte, endByte)

				stream, err := crypto.NewSeekableCTR(cfg.AESSecretKey, ivBytes, 0)
				if err != nil {
					encFile.Close()
					file.Close()
					return fmt.Errorf("NewSeekableCTR: %w", err)
				}

				partReader := io.LimitReader(file, partBytes)
				buf := make([]byte, 512*1024)
				for {
					n, rErr := partReader.Read(buf)
					if n > 0 {
						stream.XORKeyStream(buf[:n], buf[:n])
						if _, wErr := encFile.Write(buf[:n]); wErr != nil {
							encFile.Close()
							file.Close()
							return fmt.Errorf("write enc file: %w", wErr)
						}
					}
					if rErr == io.EOF {
						break
					}
					if rErr != nil {
						encFile.Close()
						file.Close()
						return fmt.Errorf("read partReader: %w", rErr)
					}
				}
				encFile.Close()

				// Upload file mã hóa lên Telegram
				log.Printf("[%s Part %d] Uploading encrypted file to Telegram...", q, partOrder)
				msgID, actualSize, err := tgClient.UploadEncryptedPart(uploadCtx, encPartPath, func(uploaded, total int64) {
					pct := float64(uploaded) * 100.0 / float64(total)
					fmt.Printf("\r  -> Progress: %.1f%% (%d / %d bytes)", pct, uploaded, total)
				})
				fmt.Println()

				if err != nil {
					file.Close()
					return fmt.Errorf("UploadEncryptedPart: %w", err)
				}

				// Xóa file mã hóa ngay sau khi upload xong để tiết kiệm đĩa
				_ = os.Remove(encPartPath)

				// Ghi vào MySQL
				part := &database.VideoPart{
					QualityID:   qualityID,
					PartOrder:   partOrder,
					TgChatID:    cfg.TgChannelID,
					TgMessageID: msgID,
					PartSize:    actualSize,
					StartByte:   startByte,
					EndByte:     endByte,
					IV:          ivHex,
				}
				if _, err := db.CreatePart(part); err != nil {
					file.Close()
					return fmt.Errorf("create part in db: %w", err)
				}
				log.Printf("[Database] Saved Part %d -> Telegram Msg ID: %d, IV: %s", partOrder, msgID, ivHex)

				offset += partBytes
				partOrder++
			}
			file.Close()
		}

		log.Println("==================================================")
		log.Printf("UPLOAD HOÀN TẤT THÀNH CÔNG!")
		log.Printf("Video ID: %d - \"%s\"", videoID, videoTitle)
		log.Printf("Xem trên web: http://localhost:%s/watch/%d", cfg.Port, videoID)
		log.Println("==================================================")
		return nil
	})

	if err != nil {
		log.Fatalf("FATAL: Upload error: %v", err)
	}
}
