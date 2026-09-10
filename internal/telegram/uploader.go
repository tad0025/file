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

	"github.com/gotd/td/tg"
)

type ProgressFunc func(uploadedBytes, totalBytes int64)

// UploadEncryptedPart upload một file đã mã hóa (.enc) lên Telegram Channel dưới dạng InputFileBig
func (c *Client) UploadEncryptedPart(ctx context.Context, filePath string, progress ProgressFunc) (int64, int64, error) {
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

	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return 0, 0, err
	}
	fileID := n.Int64()

	chunkSize := c.cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 512 * 1024
	}

	totalParts := int((fileSize + int64(chunkSize) - 1) / int64(chunkSize))
	buf := make([]byte, chunkSize)
	var uploaded int64

	log.Printf("[Uploader] Uploading %s (%d bytes, %d parts of %d KB)...", filepath.Base(filePath), fileSize, totalParts, chunkSize/1024)

	for part := 0; part < totalParts; part++ {
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

		if _, err := c.api.UploadSaveBigFilePart(ctx, req); err != nil {
			return 0, 0, fmt.Errorf("UploadSaveBigFilePart %d/%d: %w", part, totalParts, err)
		}

		uploaded += int64(bytesRead)
		if progress != nil {
			progress(uploaded, fileSize)
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

	updates, err := c.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    media,
		Message:  fmt.Sprintf("Encrypted Part: %s", filepath.Base(filePath)),
		RandomID: randID.Int64(),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("MessagesSendMedia: %w", err)
	}

	messageID := extractMessageID(updates)
	if messageID == 0 {
		return 0, 0, fmt.Errorf("failed to extract message ID from Telegram response")
	}

	log.Printf("[Uploader] Upload complete. Message ID: %d", messageID)
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
