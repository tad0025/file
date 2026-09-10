package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/tg"
)

type ProgressFunc func(uploadedBytes, totalBytes int64)
type ChunkSuccessFunc func(chunkIndex int, uploadedBytes, totalBytes int64)

// UploadEncryptedPart giữ tính tương thích ngược cho hàm gọi thông thường
func (c *Client) UploadEncryptedPart(ctx context.Context, filePath string, progress ProgressFunc) (int64, int64, error) {
	return c.UploadEncryptedPartParallel(ctx, filePath, 0, nil, 6, func(chunkIndex int, uploadedBytes, totalBytes int64) {
		if progress != nil {
			progress(uploadedBytes, totalBytes)
		}
	})
}

// UploadEncryptedPartResume hỗ trợ resume dựa trên startChunk tuần tự cũ
func (c *Client) UploadEncryptedPartResume(
	ctx context.Context,
	filePath string,
	existingFileID int64,
	startChunk int,
	onChunkSuccess ChunkSuccessFunc,
) (int64, int64, error) {
	uploaded := make(map[int]bool)
	for i := 0; i < startChunk; i++ {
		uploaded[i] = true
	}
	return c.UploadEncryptedPartParallel(ctx, filePath, existingFileID, uploaded, 6, onChunkSuccess)
}

// UploadEncryptedPartParallel upload file đã mã hóa (.enc) lên Telegram qua Worker Pool song song (mặc định 6 luồng)
func (c *Client) UploadEncryptedPartParallel(
	ctx context.Context,
	filePath string,
	existingFileID int64,
	uploadedChunks map[int]bool,
	concurrency int,
	onChunkSuccess ChunkSuccessFunc,
) (int64, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("open file %s: %w", filePath, err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, 0, err
	}
	fileSize := stat.Size()

	fileID := existingFileID
	if fileID == 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
		if err != nil {
			return 0, 0, err
		}
		fileID = n.Int64()
	}

	chunkSize := c.cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512 * 1024
	}

	totalParts := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
	if concurrency <= 0 {
		concurrency = 6
	}

	// Tính toán dung lượng đã upload trước đó nếu resume
	var initialUploadedBytes int64
	doneCount := 0
	if uploadedChunks != nil {
		for p := 0; p < totalParts; p++ {
			if uploadedChunks[p] {
				doneCount++
				partBytes := int64(chunkSize)
				if p == totalParts-1 {
					partBytes = fileSize - int64(p)*int64(chunkSize)
				}
				initialUploadedBytes += partBytes
			}
		}
	}

	if doneCount > 0 {
		log.Printf("[Uploader] Tiếp tục upload %s (%d/%d chunks đã có, còn lại %d chunks) qua %d luồng song song (File ID: %d)...",
			filepath.Base(filePath), doneCount, totalParts, totalParts-doneCount, concurrency, fileID)
	} else {
		log.Printf("[Uploader] Uploading %s (%d MB, %d chunks of %d KB) qua %d luồng song song...",
			filepath.Base(filePath), fileSize/(1024*1024), totalParts, chunkSize/1024, concurrency)
	}

	tasks := make(chan int, totalParts)
	for part := 0; part < totalParts; part++ {
		if uploadedChunks != nil && uploadedChunks[part] {
			continue
		}
		tasks <- part
	}
	close(tasks)

	var uploadedBytesCount atomic.Int64
	uploadedBytesCount.Store(initialUploadedBytes)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			buf := make([]byte, chunkSize)

			for part := range tasks {
				select {
				case <-workerCtx.Done():
					return
				default:
				}

				seekOffset := int64(part) * int64(chunkSize)
				bytesRead, rErr := file.ReadAt(buf, seekOffset)
				if rErr != nil && rErr != io.EOF && rErr != io.ErrUnexpectedEOF {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("read chunk %d: %w", part, rErr)
						cancel()
					})
					return
				}
				if bytesRead == 0 {
					continue
				}

				req := &tg.UploadSaveBigFilePartRequest{
					FileID:         fileID,
					FilePart:       part,
					FileTotalParts: totalParts,
					Bytes:          buf[:bytesRead],
				}

				// Cơ chế Auto-Retry với Exponential Backoff khi rớt mạng
				maxRetries := 12
				var saveErr error
				for attempt := 1; attempt <= maxRetries; attempt++ {
					select {
					case <-workerCtx.Done():
						return
					default:
					}

					_, saveErr = c.api.UploadSaveBigFilePart(workerCtx, req)
					if saveErr == nil {
						break
					}

					backoff := time.Duration(attempt*2) * time.Second
					if backoff > 30*time.Second {
						backoff = 30 * time.Second
					}

					log.Printf("\n[Mạng chập chờn] Lỗi upload chunk %d/%d (luồng %d): %v. Đang tự kết nối lại lần %d/%d sau %v...",
						part+1, totalParts, workerID, saveErr, attempt, maxRetries, backoff)

					select {
					case <-workerCtx.Done():
						return
					case <-time.After(backoff):
					}
				}

				if saveErr != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("UploadSaveBigFilePart chunk %d/%d thất bại sau %d lần thử: %w", part+1, totalParts, maxRetries, saveErr)
						cancel()
					})
					return
				}

				currentUploaded := uploadedBytesCount.Add(int64(bytesRead))
				if onChunkSuccess != nil {
					onChunkSuccess(part, currentUploaded, fileSize)
				}
			}
		}(w)
	}

	wg.Wait()

	if firstErr != nil {
		return 0, 0, firstErr
	}

	peer := c.GetInputPeer()
	if peer == nil {
		return 0, 0, fmt.Errorf("channel peer not resolved yet")
	}

	randID, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	media := &tg.InputMediaUploadedDocument{
		File: &tg.InputFileBig{
			ID:    fileID,
			Parts: totalParts,
			Name:  filepath.Base(filePath),
		},
		MimeType: "application/octet-stream",
	}

	// Retry gửi SendMedia nếu mạng chập chờn lúc chốt file
	var updates tg.UpdatesClass
	var sendErr error
	for attempt := 1; attempt <= 10; attempt++ {
		updates, sendErr = c.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer:     peer,
			Media:    media,
			Message:  fmt.Sprintf("Encrypted Part: %s", filepath.Base(filePath)),
			RandomID: randID.Int64(),
		})
		if sendErr == nil {
			break
		}
		if strings.Contains(sendErr.Error(), "FILE_PART_LENGTH_INVALID") || strings.Contains(sendErr.Error(), "FILE_PARTS_EMPTY") {
			return 0, 0, fmt.Errorf("FILE_PART_LENGTH_INVALID: %w", sendErr)
		}
		log.Printf("\n[Mạng chập chờn] MessagesSendMedia lỗi: %v. Thử lại sau 3s (lần %d/10)...", sendErr, attempt)
		time.Sleep(3 * time.Second)
	}

	if sendErr != nil {
		return 0, 0, fmt.Errorf("MessagesSendMedia failed: %w", sendErr)
	}

	messageID := extractMessageID(updates)
	if messageID == 0 {
		return 0, 0, fmt.Errorf("failed to extract message ID from Telegram response")
	}

	log.Printf("\n[Uploader] Upload chốt hoàn tất. Telegram Message ID: %d", messageID)
	return messageID, fileSize, nil
}

func extractMessageID(updates tg.UpdatesClass) int64 {
	switch u := updates.(type) {
	case *tg.Updates:
		for _, upd := range u.Updates {
			if m, ok := upd.(*tg.UpdateNewChannelMessage); ok {
				if msg, ok := m.Message.(*tg.Message); ok {
					return int64(msg.ID)
				}
			}
			if m, ok := upd.(*tg.UpdateNewMessage); ok {
				if msg, ok := m.Message.(*tg.Message); ok {
					return int64(msg.ID)
				}
			}
		}
	case *tg.UpdateShortSentMessage:
		return int64(u.ID)
	}
	return 0
}
