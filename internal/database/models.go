package database

import "time"

type Video struct {
	ID        int64          `json:"id"`
	Title     string         `json:"title"`
	CreatedAt time.Time      `json:"created_at"`
	Qualities []VideoQuality `json:"qualities,omitempty"`
}

type VideoQuality struct {
	ID          int64       `json:"id"`
	VideoID     int64       `json:"video_id"`
	Quality     string      `json:"quality"` // 'original', '1080p', '720p'
	FileName    string      `json:"file_name"`
	MimeType    string      `json:"mime_type"`
	TotalSize   int64       `json:"total_size"`
	IsMultiPart bool        `json:"is_multi_part"`
	Parts       []VideoPart `json:"parts,omitempty"`
}

type VideoPart struct {
	ID          int64  `json:"id"`
	QualityID   int64  `json:"quality_id"`
	PartOrder   int    `json:"part_order"`
	TgChatID    int64  `json:"tg_chat_id"`
	TgMessageID int64  `json:"tg_message_id"`
	PartSize    int64  `json:"part_size"`
	StartByte   int64  `json:"start_byte"`
	EndByte     int64  `json:"end_byte"`
	IV          string `json:"iv"` // 16 bytes hex string
}
