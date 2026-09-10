package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"videostream/internal/config"
	"videostream/internal/crypto"
	"videostream/internal/database"
	"videostream/internal/ffmpeg"
	"videostream/internal/telegram"
)

func main() {
	filePathFlag := flag.String("file", "", "Đường dẫn tới file video gốc (Tùy chọn)")
	titleFlag := flag.String("title", "", "Tiêu đề hiển thị cho video (Tùy chọn)")
	keepTranscodes := flag.Bool("keep-transcodes", true, "Giữ lại các file video sau khi transcode tại thư mục video gốc (mặc định true)")
	flag.Parse()

	log.Println("==================================================")
	log.Println("TeleStream Console Video Uploader & Transcoder")
	log.Println("==================================================")

	scanner := bufio.NewScanner(os.Stdin)
	resolvedPath := strings.TrimSpace(*filePathFlag)

	// Nếu không truyền qua cờ lệnh, nhắc người dùng nhập tương tác trên Console
	if resolvedPath == "" {
		fmt.Print(">> Nhập đường dẫn file video (kéo thả file vào đây hoặc paste đường dẫn): ")
		if scanner.Scan() {
			resolvedPath = strings.TrimSpace(scanner.Text())
		}
	}

	// Loại bỏ dấu nháy kép hoặc đơn do Windows tự thêm khi kéo thả đường dẫn
	resolvedPath = strings.Trim(resolvedPath, "\"'")

	if resolvedPath == "" {
		log.Fatalf("Lỗi: Đường dẫn file video không được để trống.")
	}

	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		log.Fatalf("Lỗi đường dẫn file: %v", err)
	}

	stat, err := os.Stat(absPath)
	if err != nil {
		log.Fatalf("File không tồn tại: %v", err)
	}

	videoTitle := strings.TrimSpace(*titleFlag)
	if videoTitle == "" {
		defaultTitle := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
		fmt.Printf(">> Nhập tiêu đề video (bấm Enter để lấy mặc định '%s'): ", defaultTitle)
		if scanner.Scan() {
			customTitle := strings.TrimSpace(scanner.Text())
			if customTitle != "" {
				videoTitle = customTitle
			} else {
				videoTitle = defaultTitle
			}
		} else {
			videoTitle = defaultTitle
		}
	}

	fmt.Println("--------------------------------------------------")
	log.Printf("File gốc:  %s (%d MB)", absPath, stat.Size()/(1024*1024))
	log.Printf("Tiêu đề:   %s", videoTitle)

	// 1. Nạp cấu hình & kiểm tra biến môi trường
	cfg := config.MustLoad()

	// 2. Kiểm tra công cụ FFmpeg
	if err := ffmpeg.CheckFFmpeg(); err != nil {
		log.Fatalf("FATAL: %v. Hãy cài đặt FFmpeg và thêm vào PATH.", err)
	}

	// 3. Kết nối MySQL Aiven
	log.Println("[MySQL] Kết nối cơ sở dữ liệu Aiven...")
	db, err := database.Connect(cfg.MySQLURL)
	if err != nil {
		log.Fatalf("FATAL: Kết nối MySQL thất bại: %v", err)
	}
	defer db.Close()

	// 4. Khởi tạo thư mục transcode NGAY TẠI VỊ TRÍ FILE GỐC
	videoDir := filepath.Dir(absPath)
	baseName := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	transcodeDir := filepath.Join(videoDir, baseName+"_transcoded")
	if err := os.MkdirAll(transcodeDir, 0755); err != nil {
		log.Fatalf("Tạo thư mục lưu file transcode thất bại: %v", err)
	}
	log.Printf("[FFmpeg] Thư mục lưu các file chuẩn hóa: %s", transcodeDir)

	defer func() {
		if !*keepTranscodes {
			_ = os.RemoveAll(transcodeDir)
		}
	}()

	// 5. Transcode 3 phiên bản độ phân giải (1080p, 720p) lưu ngay tại thư mục đó
	log.Println("[FFmpeg] Bắt đầu chuẩn hóa độ phân giải (original, 1080p, 720p)...")
	transcodedFiles, err := ffmpeg.TranscodeAll(absPath, transcodeDir)
	if err != nil {
		log.Fatalf("Transcode video thất bại: %v", err)
	}

	log.Println("[FFmpeg] Hoàn tất chuẩn hóa video:")
	for q, fPath := range transcodedFiles {
		fStat, _ := os.Stat(fPath)
		var sz int64
		if fStat != nil {
			sz = fStat.Size()
		}
		log.Printf("  • Bản %-8s: %s (%d MB)", q, fPath, sz/(1024*1024))
	}

	// 6. Khởi tạo Telegram Client MTProto
	log.Println("[Telegram] Đang kết nối tài khoản MTProto Telegram...")
	tgClient, err := telegram.NewClient(cfg)
	if err != nil {
		log.Fatalf("Khởi tạo Telegram client thất bại: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Chạy quy trình upload bên trong Run loop của gotd
	err = tgClient.Run(ctx, func(uploadCtx context.Context) error {
		time.Sleep(2 * time.Second)

		// Tạo bản ghi Video trong MySQL
		videoID, err := db.CreateVideo(videoTitle)
		if err != nil {
			return fmt.Errorf("create video in db: %w", err)
		}
		log.Printf("[Database] Đã tạo Video ID: %d trong MySQL", videoID)

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

			const maxPartBytes = int64(2000000000) // 2GB ngưỡng Telegram
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
			log.Printf("[Database] Đã tạo bản '%s' (ID: %d, %d MB)", q, qualityID, qTotalSize/(1024*1024))

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

				ivBytes, ivHex, err := crypto.GenerateIV()
				if err != nil {
					file.Close()
					return fmt.Errorf("generate IV: %w", err)
				}

				// File mã hóa tạm
				encPartPath := filepath.Join(transcodeDir, fmt.Sprintf("%s_part%d.enc", q, partOrder))
				encFile, err := os.Create(encPartPath)
				if err != nil {
					file.Close()
					return fmt.Errorf("create enc file: %w", err)
				}

				log.Printf("[%s Part %d] Đang mã hóa AES-256-CTR...", q, partOrder)

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
				log.Printf("[%s Part %d] Đang tải file mã hóa lên Telegram Channel...", q, partOrder)
				msgID, actualSize, err := tgClient.UploadEncryptedPart(uploadCtx, encPartPath, func(uploaded, total int64) {
					pct := float64(uploaded) * 100.0 / float64(total)
					mbUploaded := float64(uploaded) / (1024 * 1024)
					mbTotal := float64(total) / (1024 * 1024)
					fmt.Printf("\r  -> Tiến độ Upload: %.1f%% (%.1f / %.1f MB)", pct, mbUploaded, mbTotal)
				})
				fmt.Println()

				if err != nil {
					file.Close()
					return fmt.Errorf("UploadEncryptedPart: %w", err)
				}

				// Xóa file mã hóa ngay sau khi upload xong để giải phóng đĩa
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
				log.Printf("[Database] Đã lưu Part %d -> Telegram Msg ID: %d", partOrder, msgID)

				offset += partBytes
				partOrder++
			}
			file.Close()
		}

		log.Println("==================================================")
		log.Printf("UPLOAD HOÀN TẤT THÀNH CÔNG!")
		log.Printf("Video: \"%s\" (ID: %d)", videoTitle, videoID)
		log.Printf("Xem trên Web Back4App: https://videostreamtele1-tuciuxxx.b4a.run/watch/%d", videoID)
		log.Println("==================================================")
		return nil
	})

	if err != nil {
		log.Fatalf("FATAL: Upload error: %v", err)
	}

	fmt.Println("\n>> Bấm phím Enter để kết thúc...")
	scanner.Scan()
}
