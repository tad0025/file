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
	"time"

	"github.com/gotd/td/tg"
)

type ProgressFunc func(uploadedBytes, totalBytes int64)
type ChunkSuccessFunc func(chunkIndex int, uploadedBytes, totalBytes int64)

// UploadEncryptedPart giữ tính tương thích ngược cho hàm gọi thông thường
func (c *Client) UploadEncryptedPart(ctx context.Context, filePath string, progress ProgressFunc) (int64, int64, error) {
	return c.UploadEncryptedPartResume(ctx, filePath, 0, 0, func(chunkIndex int, uploadedBytes, totalBytes int64) {
		if progress != nil {
			progress(uploadedBytes, totalBytes)
		}
	})
}

// UploadEncryptedPartResume upload file đã mã hóa (.enc) lên Telegram có hỗ trợ Resume và Auto-Retry khi rớt mạng
func (c *Client) UploadEncryptedPartResume(
	ctx context.Context,
	filePath string,
	existingFileID int64,
	startChunk int,
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
	buf := make([]byte, chunkSize)

	if startChunk > 0 {
		log.Printf("[Uploader] Tiếp tục upload dở dang %s từ chunk %d/%d (File ID: %d)...",
			filepath.Base(filePath), startChunk+1, totalParts, fileID)
	} else {
		log.Printf("[Uploader] Uploading %s (%d MB, %d chunks of %d KB)...",
			filepath.Base(filePath), fileSize/(1024*1024), totalParts, chunkSize/1024)
	}

	for part := startChunk; part < totalParts; part++ {
		// Seek tới vị trí chunk tương ứng
		seekOffset := int64(part) * int64(chunkSize)
		if _, err := file.Seek(seekOffset, io.SeekStart); err != nil {
			return 0, 0, fmt.Errorf("seek file to offset %d: %w", seekOffset, err)
		}

		bytesRead, err := io.ReadFull(file, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, 0, fmt.Errorf("read chunk part %d: %w", part, err)
		}

		if bytesRead == 0 {
			break
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
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			default:
			}

			_, saveErr = c.api.UploadSaveBigFilePart(ctx, req)
			if saveErr == nil {
				break
			}

			backoff := time.Duration(attempt*2) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}

			log.Printf("\n[Mạng chập chờn] Lỗi upload chunk %d/%d: %v. Đang tự kết nối lại lần %d/%d sau %v...",
				part+1, totalParts, saveErr, attempt, maxRetries, backoff)

			select {
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			case <-time.After(backoff):
			}
		}

		if saveErr != nil {
			return 0, 0, fmt.Errorf("UploadSaveBigFilePart chunk %d/%d thất bại sau %d lần thử: %w", part+1, totalParts, maxRetries, saveErr)
		}

		uploaded := seekOffset + int64(bytesRead)
		if onChunkSuccess != nil {
			onChunkSuccess(part, uploaded, fileSize)
		}
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
