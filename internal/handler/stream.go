package handler

import (
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"videostream/internal/config"
	"videostream/internal/crypto"
	"videostream/internal/database"
	"videostream/internal/telegram"
)

type StreamHandler struct {
	db         *database.DB
	downloader *telegram.Downloader
	cfg        *config.Config
}

func NewStreamHandler(db *database.DB, downloader *telegram.Downloader, cfg *config.Config) *StreamHandler {
	return &StreamHandler{
		db:         db,
		downloader: downloader,
		cfg:        cfg,
	}
}

// ServeHTTP xử lý HTTP Range Requests cho endpoint /api/stream/{quality_id}
func (h *StreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Trích xuất quality_id từ URL
	path := strings.TrimPrefix(r.URL.Path, "/api/stream/")
	qualityID, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "Invalid quality ID", http.StatusBadRequest)
		return
	}

	quality, err := h.db.GetQualityWithParts(qualityID)
	if err != nil {
		http.Error(w, "Video quality not found", http.StatusNotFound)
		return
	}

	if len(quality.Parts) == 0 {
		http.Error(w, "No parts available for this quality", http.StatusNotFound)
		return
	}

	totalSize := quality.TotalSize
	rangeHeader := r.Header.Get("Range")

	var start, end int64

	if rangeHeader == "" {
		// Yêu cầu phát từ đầu nếu không có Range header
		start = 0
		end = totalSize - 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.Header().Set("Content-Type", quality.MimeType)
		w.WriteHeader(http.StatusOK)
	} else {
		// Xử lý Range: bytes=start-end
		if !strings.HasPrefix(rangeHeader, "bytes=") {
			http.Error(w, "Invalid Range Header", http.StatusRequestedRangeNotSatisfiable)
			return
		}

		rangeVal := strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.Split(rangeVal, "-")

		if len(parts) != 2 {
			http.Error(w, "Invalid Range Format", http.StatusRequestedRangeNotSatisfiable)
			return
		}

		if parts[0] == "" {
			// Suffix byte range: bytes=-500
			suffixLen, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				http.Error(w, "Invalid Range value", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			start = totalSize - suffixLen
			end = totalSize - 1
		} else {
			start, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				http.Error(w, "Invalid Range start", http.StatusRequestedRangeNotSatisfiable)
				return
			}

			if parts[1] == "" {
				end = totalSize - 1
			} else {
				end, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					http.Error(w, "Invalid Range end", http.StatusRequestedRangeNotSatisfiable)
					return
				}
			}
		}

		if start < 0 || start >= totalSize || end < start || end >= totalSize {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
			http.Error(w, "Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}

		contentLength := end - start + 1
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.Header().Set("Content-Type", quality.MimeType)
		w.WriteHeader(http.StatusPartialContent)
	}

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	ctx := r.Context()
	// Cỡ chunk stream tối ưu 128KB (đảm bảo kèm padding căn lề không vượt trần 256KB của MTProto)
	chunkSize := 128 * 1024

	// Ghép ảo các Part (Virtual Concatenation)
	for _, part := range quality.Parts {
		// Kiểm tra part có nằm trong dải [start, end]
		if part.EndByte < start || part.StartByte > end {
			continue
		}

		// Tính toán khoảng byte cục bộ trong part này
		partStart := start
		if part.StartByte > partStart {
			partStart = part.StartByte
		}

		partEnd := end
		if part.EndByte < partEnd {
			partEnd = part.EndByte
		}

		localStart := partStart - part.StartByte
		localEnd := partEnd - part.StartByte

		iv, err := hex.DecodeString(part.IV)
		if err != nil || len(iv) != 16 {
			log.Printf("[Stream] Invalid IV hex string for part %d: %v", part.PartOrder, err)
			return
		}

		stream, err := crypto.NewSeekableCTR(h.cfg.AESSecretKey, iv, localStart)
		if err != nil {
			log.Printf("[Stream] NewSeekableCTR failed: %v", err)
			return
		}

		curOffset := localStart
		for curOffset <= localEnd {
			select {
			case <-ctx.Done():
				// Client đã ngắt kết nối (tua hoặc đóng tab)
				return
			default:
			}

			readLimit := int64(chunkSize)
			if remaining := localEnd - curOffset + 1; remaining < readLimit {
				readLimit = remaining
			}

			chunk, err := h.downloader.ReadChunk(ctx, part.TgChatID, part.TgMessageID, curOffset, int(readLimit))
			if err != nil {
				if ctx.Err() == nil && !strings.Contains(err.Error(), "context canceled") {
					log.Printf("[Stream] ReadChunk error part %d (offset %d): %v", part.PartOrder, curOffset, err)
				}
				return
			}

			if len(chunk) == 0 {
				break
			}

			// Giải mã on-the-fly trên RAM
			stream.XORKeyStream(chunk, chunk)

			// Ghi dữ liệu thô ra HTTP response
			if _, err := w.Write(chunk); err != nil {
				return
			}

			if flusher != nil {
				flusher.Flush()
			}

			curOffset += int64(len(chunk))
		}
	}
}
