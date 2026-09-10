package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

type docCacheItem struct {
	location *tg.InputDocumentFileLocation
	expireAt time.Time
}

type Downloader struct {
	client  *Client
	cacheMu sync.RWMutex
	cache   map[int64]*docCacheItem
}

func NewDownloader(client *Client) *Downloader {
	return &Downloader{
		client: client,
		cache:  make(map[int64]*docCacheItem),
	}
}

// GetDocumentLocation lấy và cache InputDocumentFileLocation từ MessageID
func (d *Downloader) GetDocumentLocation(ctx context.Context, chatID, messageID int64) (*tg.InputDocumentFileLocation, error) {
	d.cacheMu.RLock()
	item, found := d.cache[messageID]
	d.cacheMu.RUnlock()

	if found && time.Now().Before(item.expireAt) {
		return item.location, nil
	}

	peer := d.client.GetInputPeer()
	if peer == nil {
		return nil, fmt.Errorf("channel peer not resolved")
	}

	channelPeer, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return nil, fmt.Errorf("peer is not channel")
	}

	messages, err := d.client.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  channelPeer.ChannelID,
			AccessHash: channelPeer.AccessHash,
		},
		ID: []tg.InputMessageClass{&tg.InputMessageID{ID: int(messageID)}},
	})
	if err != nil {
		return nil, fmt.Errorf("ChannelsGetMessages msg %d: %w", messageID, err)
	}

	var msg *tg.Message
	switch m := messages.(type) {
	case *tg.MessagesChannelMessages:
		for _, msgClass := range m.Messages {
			if message, ok := msgClass.(*tg.Message); ok && int64(message.ID) == messageID {
				msg = message
				break
			}
		}
	case *tg.MessagesMessages:
		for _, msgClass := range m.Messages {
			if message, ok := msgClass.(*tg.Message); ok && int64(message.ID) == messageID {
				msg = message
				break
			}
		}
	}

	if msg == nil {
		return nil, fmt.Errorf("message %d not found in channel", messageID)
	}

	mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, fmt.Errorf("message %d does not contain document media", messageID)
	}

	doc, ok := mediaDoc.Document.(*tg.Document)
	if !ok {
		return nil, fmt.Errorf("document in message %d is empty/unsupported", messageID)
	}

	loc := &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}

	d.cacheMu.Lock()
	d.cache[messageID] = &docCacheItem{
		location: loc,
		expireAt: time.Now().Add(2 * time.Hour), // File reference tồn tại nhiều giờ
	}
	d.cacheMu.Unlock()

	return loc, nil
}

// ReadChunk đọc một khối byte từ document trên Telegram, tự động căn lề 4096 bytes theo chuẩn MTProto
func (d *Downloader) ReadChunk(ctx context.Context, chatID, messageID int64, offset int64, limit int) ([]byte, error) {
	loc, err := d.GetDocumentLocation(ctx, chatID, messageID)
	if err != nil {
		return nil, err
	}

	// MTProto upload.getFile yêu cầu offset và limit chia hết cho 4096 (4KB)
	const align = 4096
	alignedOffset := (offset / align) * align
	discardPrefix := int(offset - alignedOffset)
	neededTotal := discardPrefix + limit

	alignedLimit := ((neededTotal + align - 1) / align) * align
	if alignedLimit > 1024*1024 { // tối đa 1MB theo MTProto spec
		alignedLimit = 1024 * 1024
	}

	req := &tg.UploadGetFileRequest{
		Location: loc,
		Offset:   alignedOffset,
		Limit:    alignedLimit,
	}

	res, err := d.client.api.UploadGetFile(ctx, req)
	if err != nil {
		// Xóa cache file_reference nếu bị hết hạn để thử fetch lại lần sau
		d.cacheMu.Lock()
		delete(d.cache, messageID)
		d.cacheMu.Unlock()
		return nil, fmt.Errorf("UploadGetFile offset %d: %w", offset, err)
	}

	var bytes []byte
	switch r := res.(type) {
	case *tg.UploadFile:
		bytes = r.Bytes
	case *tg.UploadFileCDNRedirect:
		return nil, fmt.Errorf("CDN redirect not supported")
	}

	if len(bytes) <= discardPrefix {
		return nil, fmt.Errorf("returned bytes length %d <= discard %d", len(bytes), discardPrefix)
	}

	available := len(bytes) - discardPrefix
	if available > limit {
		available = limit
	}

	return bytes[discardPrefix : discardPrefix+available], nil
}
