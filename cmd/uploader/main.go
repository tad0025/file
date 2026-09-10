package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"videostream/internal/config"
	"videostream/internal/crypto"
	"videostream/internal/database"
	"videostream/internal/ffmpeg"
	"videostream/internal/telegram"
)

// UploadState lưu trạng thái checkpoint phục hồi khi cúp điện / tắt máy ngang
type UploadState struct {
	VideoID   int64                    `json:"video_id"`
	Title     string                   `json:"title"`
	Qualities map[string]*QualityState `json:"qualities"`
}

type QualityState struct {
	QualityID int64              `json:"quality_id"`
	TotalSize int64              `json:"total_size"`
	Done      bool               `json:"done"`
	Parts     map[int]*PartState `json:"parts"`
}

type PartState struct {
	PartOrder         int    `json:"part_order"`
	FileID            int64  `json:"file_id"`
	StartByte         int64  `json:"start_byte"`
	EndByte           int64  `json:"end_byte"`
	PartSize          int64  `json:"part_size"`
	IV                string `json:"iv"`
	LastUploadedChunk int          `json:"last_uploaded_chunk"`
	UploadedChunks    map[int]bool `json:"uploaded_chunks,omitempty"`
	TgMessageID       int64        `json:"tg_message_id"`
	Done              bool         `json:"done"`
}

func loadState(path string) (*UploadState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state UploadState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveState(path string, state *UploadState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func main() {
	filePathFlag := flag.String("file", "", "Đường dẫn tới file video gốc (Tùy chọn)")
	titleFlag := flag.String("title", "", "Tiêu đề hiển thị cho video (Tùy chọn)")
	keepTranscodes := flag.Bool("keep-transcodes", true, "Giữ lại các file video sau khi transcode tại thư mục video gốc (mặc định true)")
	flag.Parse()

	log.Println("==================================================")
	log.Println("TeleStream Video Uploader (Checkpoint & Auto-Resume)")
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

	// 4. Khởi tạo thư mục transcode tại vị trí file gốc
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

	// Đọc checkpoint state nếu có
	statePath := filepath.Join(transcodeDir, ".upload_state.json")
	state, _ := loadState(statePath)
	if state != nil {
		log.Printf("[Checkpoint] Phát hiện tiến trình dở dang trước đó (Video ID: %d). Sẽ tiếp tục upload từ điểm bị ngắt!", state.VideoID)
	} else {
		state = &UploadState{
			Title:     videoTitle,
			Qualities: make(map[string]*QualityState),
		}
	}

	// 5. Transcode các phiên bản độ phân giải (Tự động bỏ qua nếu file đã tồn tại)
	log.Println("[FFmpeg] Kiểm tra & chuẩn hóa độ phân giải (original, 1080p, 720p)...")
	transcodedFiles, err := ffmpeg.TranscodeAll(absPath, transcodeDir)
	if err != nil {
		log.Fatalf("Transcode video thất bại: %v", err)
	}

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

	err = tgClient.Run(ctx, func(uploadCtx context.Context) error {
		time.Sleep(2 * time.Second)

		// Nếu chưa có VideoID trong state, tạo mới trong MySQL
		if state.VideoID == 0 {
			videoID, err := db.CreateVideo(videoTitle)
			if err != nil {
				return fmt.Errorf("create video in db: %w", err)
			}
			state.VideoID = videoID
			_ = saveState(statePath, state)
			log.Printf("[Database] Đã tạo Video ID mới: %d", videoID)
		} else {
			log.Printf("[Database] Dùng lại Video ID từ checkpoint: %d", state.VideoID)
		}

		qualityOrder := []string{"original", "1080p", "720p"}

		for _, q := range qualityOrder {
			qFilePath, exists := transcodedFiles[q]
			if !exists {
				continue
			}

			qState, exists := state.Qualities[q]
			if !exists {
				qState = &QualityState{
					Parts: make(map[int]*PartState),
				}
				state.Qualities[q] = qState
			}

			if qState.Done {
				log.Printf("[Checkpoint] Bản '%s' đã hoàn thành upload trước đó, bỏ qua.", q)
				continue
			}

			qStat, err := os.Stat(qFilePath)
			if err != nil {
				return fmt.Errorf("stat file %s: %w", qFilePath, err)
			}
			qTotalSize := qStat.Size()
			qState.TotalSize = qTotalSize

			const maxPartBytes = int64(2000000000) // 2GB
			isMultiPart := qTotalSize > maxPartBytes

			// Tạo Quality trong DB nếu chưa có
			if qState.QualityID == 0 {
				vq := &database.VideoQuality{
					VideoID:     state.VideoID,
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
				qState.QualityID = qualityID
				_ = saveState(statePath, state)
				log.Printf("[Database] Đã tạo Quality '%s' (ID: %d)", q, qualityID)
			}

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

				pState, exists := qState.Parts[partOrder]
				if !exists {
					pState = &PartState{
						PartOrder:         partOrder,
						StartByte:         startByte,
						EndByte:           endByte,
						PartSize:          partBytes,
						LastUploadedChunk: -1,
					}
					qState.Parts[partOrder] = pState
				}

				if pState.Done {
					log.Printf("[Checkpoint] [%s Part %d] Đã hoàn thành (Telegram Msg %d), bỏ qua.", q, partOrder, pState.TgMessageID)
					offset += partBytes
					partOrder++
					continue
				}

				// Sinh hoặc lấy lại IV
				var ivBytes []byte
				if pState.IV == "" {
					var ivHex string
					ivBytes, ivHex, err = crypto.GenerateIV()
					if err != nil {
						file.Close()
						return fmt.Errorf("generate IV: %w", err)
					}
					pState.IV = ivHex
					_ = saveState(statePath, state)
				} else {
					ivBytes, _ = hex.DecodeString(pState.IV)
				}

				// File mã hóa tạm cho part này
				encPartPath := filepath.Join(transcodeDir, fmt.Sprintf("%s_part%d.enc", q, partOrder))
				
				// Nếu file mã hóa chưa có, tạo mới
				if _, err := os.Stat(encPartPath); os.IsNotExist(err) {
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

					if _, err := file.Seek(offset, io.SeekStart); err != nil {
						encFile.Close()
						file.Close()
						return fmt.Errorf("seek source file: %w", err)
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
				}

				// Khởi tạo FileID cố định cho part này nếu chưa có và lưu ngay vào checkpoint
				if pState.FileID == 0 {
					n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
					if err != nil {
						file.Close()
						return fmt.Errorf("generate fileID: %w", err)
					}
					pState.FileID = n.Int64()
					_ = saveState(statePath, state)
				}

				// Khởi tạo map chunk nếu chưa có
				if pState.UploadedChunks == nil {
					pState.UploadedChunks = make(map[int]bool)
					if pState.LastUploadedChunk >= 0 {
						for i := 0; i <= pState.LastUploadedChunk; i++ {
							pState.UploadedChunks[i] = true
						}
					}
				}

				var stateMu sync.Mutex
				var lastSaveTime time.Time
				var chunksSinceSave int
				var msgID, actualSize int64

				// Upload với Worker Pool song song (6 luồng), Resume và Auto-Retry
				for uploadAttempt := 1; uploadAttempt <= 2; uploadAttempt++ {
					msgID, actualSize, err = tgClient.UploadEncryptedPartParallel(
						uploadCtx,
						encPartPath,
						pState.FileID,
						pState.UploadedChunks,
						6,
						func(chunkIndex int, uploaded, total int64) {
							stateMu.Lock()
							defer stateMu.Unlock()

							pState.UploadedChunks[chunkIndex] = true
							pState.LastUploadedChunk = chunkIndex
							chunksSinceSave++

							pct := float64(uploaded) * 100.0 / float64(total)
							mbUploaded := float64(uploaded) / (1024 * 1024)
							mbTotal := float64(total) / (1024 * 1024)
							fmt.Printf("\r  -> [%s Part %d] Tiến độ: %.1f%% (%.1f / %.1f MB) [%d chunks đã xong] (6 luồng)",
								q, partOrder, pct, mbUploaded, mbTotal, len(pState.UploadedChunks))

							now := time.Now()
							if chunksSinceSave >= 10 || now.Sub(lastSaveTime) > 3*time.Second {
								_ = saveState(statePath, state)
								lastSaveTime = now
								chunksSinceSave = 0
							}
						},
					)
					fmt.Println()

					if err != nil && strings.Contains(err.Error(), "FILE_PART_LENGTH_INVALID") && uploadAttempt == 1 {
						log.Printf("[Uploader] Chunks trên máy chủ Telegram không đồng bộ (do lệch File ID). Tự động cấp FileID mới và đẩy lại %s Part %d...", q, partOrder)
						n, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
						pState.FileID = n.Int64()
						pState.UploadedChunks = make(map[int]bool)
						pState.LastUploadedChunk = -1
						_ = saveState(statePath, state)
						continue
					}
					break
				}

				if err != nil {
					file.Close()
					return fmt.Errorf("UploadEncryptedPartParallel: %w", err)
				}

				// Xóa file mã hóa ngay sau khi upload xong
				_ = os.Remove(encPartPath)

				// Ghi nhận vào MySQL
				part := &database.VideoPart{
					QualityID:   qState.QualityID,
					PartOrder:   partOrder,
					TgChatID:    cfg.TgChannelID,
					TgMessageID: msgID,
					PartSize:    actualSize,
					StartByte:   startByte,
					EndByte:     endByte,
					IV:          pState.IV,
				}
				if _, err := db.CreatePart(part); err != nil {
					file.Close()
					return fmt.Errorf("create part in db: %w", err)
				}

				pState.TgMessageID = msgID
				pState.PartSize = actualSize
				pState.Done = true
				_ = saveState(statePath, state)
				log.Printf("[Database] Đã lưu Part %d -> Telegram Msg ID: %d", partOrder, msgID)

				offset += partBytes
				partOrder++
			}
			file.Close()

			qState.Done = true
			_ = saveState(statePath, state)
		}

		// Xóa file checkpoint khi toàn bộ các bản đã hoàn tất
		_ = os.Remove(statePath)

		log.Println("==================================================")
		log.Printf("TẤT CẢ CÁC BẢN ĐÃ UPLOAD THÀNH CÔNG!")
		log.Printf("Video ID: %d - \"%s\"", state.VideoID, videoTitle)
		log.Printf("Xem trên Web Back4App: https://videostreamtele1-tuciuxxx.b4a.run/watch/%d", state.VideoID)
		log.Println("==================================================")
		return nil
	})

	if err != nil {
		log.Fatalf("FATAL: Upload error: %v", err)
	}

	fmt.Println("\n>> Bấm phím Enter để kết thúc...")
	scanner.Scan()
}
