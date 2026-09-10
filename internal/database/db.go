package database

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type DB struct {
	*sql.DB
}

// ConvertURLToDSN chuyển đổi mysql:// URL sang DSN của go-sql-driver/mysql
func ConvertURLToDSN(rawURL string) (string, error) {
	if !strings.HasPrefix(rawURL, "mysql://") && !strings.HasPrefix(rawURL, "mysql:") {
		return rawURL, nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL URL: %w", err)
	}

	user := u.User.Username()
	password, _ := u.User.Password()
	host := u.Host
	dbName := strings.TrimPrefix(u.Path, "/")

	// Đăng ký custom TLS để hỗ trợ SSL Aiven
	_ = mysql.RegisterTLSConfig("custom", &tls.Config{
		InsecureSkipVerify: true,
	})

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?tls=custom&parseTime=true&multiStatements=true",
		user, password, host, dbName)

	return dsn, nil
}

func Connect(mysqlURL string) (*DB, error) {
	dsn, err := ConvertURLToDSN(mysqlURL)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open mysql: %w", err)
	}

	// Tối ưu connection pool nhỏ gọn cho container 256MB RAM
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping mysql: %w", err)
	}

	database := &DB{DB: db}
	if err := database.Migrate(); err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return database, nil
}

func (db *DB) Migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS videos (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			title VARCHAR(255) NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,

		`CREATE TABLE IF NOT EXISTS video_qualities (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			video_id BIGINT NOT NULL,
			quality VARCHAR(20) NOT NULL,
			file_name VARCHAR(255) NOT NULL,
			mime_type VARCHAR(100) DEFAULT 'video/mp4',
			total_size BIGINT NOT NULL,
			is_multi_part BOOLEAN DEFAULT FALSE,
			FOREIGN KEY (video_id) REFERENCES videos(id) ON DELETE CASCADE,
			UNIQUE KEY uq_video_quality (video_id, quality)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,

		`CREATE TABLE IF NOT EXISTS video_parts (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			quality_id BIGINT NOT NULL,
			part_order INT NOT NULL,
			tg_chat_id BIGINT NOT NULL,
			tg_message_id BIGINT NOT NULL,
			part_size BIGINT NOT NULL,
			start_byte BIGINT NOT NULL,
			end_byte BIGINT NOT NULL,
			iv VARCHAR(32) NOT NULL,
			FOREIGN KEY (quality_id) REFERENCES video_qualities(id) ON DELETE CASCADE,
			INDEX idx_quality_parts (quality_id, part_order)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("exec query '%s': %w", q, err)
		}
	}

	return nil
}

func (db *DB) GetAllVideos() ([]Video, error) {
	query := `
		SELECT v.id, v.title, v.created_at,
		       COALESCE(q.id, 0), COALESCE(q.quality, ''), COALESCE(q.file_name, ''),
		       COALESCE(q.mime_type, 'video/mp4'), COALESCE(q.total_size, 0), COALESCE(q.is_multi_part, false)
		FROM videos v
		LEFT JOIN video_qualities q ON v.id = q.video_id
		ORDER BY v.id DESC, q.id ASC
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	videoMap := make(map[int64]*Video)
	var orderedIDs []int64

	for rows.Next() {
		var vID int64
		var title string
		var createdAt time.Time
		var qID int64
		var quality, fileName, mimeType string
		var totalSize int64
		var isMultiPart bool

		if err := rows.Scan(&vID, &title, &createdAt, &qID, &quality, &fileName, &mimeType, &totalSize, &isMultiPart); err != nil {
			return nil, err
		}

		v, exists := videoMap[vID]
		if !exists {
			v = &Video{
				ID:        vID,
				Title:     title,
				CreatedAt: createdAt,
				Qualities: []VideoQuality{},
			}
			videoMap[vID] = v
			orderedIDs = append(orderedIDs, vID)
		}

		if qID > 0 {
			v.Qualities = append(v.Qualities, VideoQuality{
				ID:          qID,
				VideoID:     vID,
				Quality:     quality,
				FileName:    fileName,
				MimeType:    mimeType,
				TotalSize:   totalSize,
				IsMultiPart: isMultiPart,
			})
		}
	}

	result := make([]Video, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		result = append(result, *videoMap[id])
	}
	return result, nil
}

func (db *DB) GetVideoWithQualities(videoID int64) (*Video, error) {
	var v Video
	err := db.QueryRow("SELECT id, title, created_at FROM videos WHERE id = ?", videoID).
		Scan(&v.ID, &v.Title, &v.CreatedAt)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, video_id, quality, file_name, mime_type, total_size, is_multi_part
		FROM video_qualities WHERE video_id = ? ORDER BY id ASC`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var q VideoQuality
		if err := rows.Scan(&q.ID, &q.VideoID, &q.Quality, &q.FileName, &q.MimeType, &q.TotalSize, &q.IsMultiPart); err != nil {
			return nil, err
		}
		v.Qualities = append(v.Qualities, q)
	}

	return &v, nil
}

func (db *DB) GetQualityWithParts(qualityID int64) (*VideoQuality, error) {
	var q VideoQuality
	err := db.QueryRow(`
		SELECT id, video_id, quality, file_name, mime_type, total_size, is_multi_part
		FROM video_qualities WHERE id = ?`, qualityID).
		Scan(&q.ID, &q.VideoID, &q.Quality, &q.FileName, &q.MimeType, &q.TotalSize, &q.IsMultiPart)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, quality_id, part_order, tg_chat_id, tg_message_id, part_size, start_byte, end_byte, iv
		FROM video_parts WHERE quality_id = ? ORDER BY part_order ASC`, qualityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var p VideoPart
		if err := rows.Scan(&p.ID, &p.QualityID, &p.PartOrder, &p.TgChatID, &p.TgMessageID, &p.PartSize, &p.StartByte, &p.EndByte, &p.IV); err != nil {
			return nil, err
		}
		q.Parts = append(q.Parts, p)
	}

	return &q, nil
}

func (db *DB) CreateVideo(title string) (int64, error) {
	res, err := db.Exec("INSERT INTO videos (title) VALUES (?)", title)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) CreateQuality(vq *VideoQuality) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO video_qualities (video_id, quality, file_name, mime_type, total_size, is_multi_part)
		VALUES (?, ?, ?, ?, ?, ?)`,
		vq.VideoID, vq.Quality, vq.FileName, vq.MimeType, vq.TotalSize, vq.IsMultiPart)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) CreatePart(vp *VideoPart) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO video_parts (quality_id, part_order, tg_chat_id, tg_message_id, part_size, start_byte, end_byte, iv)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		vp.QualityID, vp.PartOrder, vp.TgChatID, vp.TgMessageID, vp.PartSize, vp.StartByte, vp.EndByte, vp.IV)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
