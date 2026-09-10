package config

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	TgAPIID         int
	TgAPIHash       string
	TgSessionString string
	TgChannelID     int64
	MySQLURL        string
	JWTSecret       []byte
	AESSecretKey    []byte
	Password        string
	Port            string
	ChunkSize       int
}

func mustGetEnv(key string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		log.Fatalf("FATAL: Environment variable '%s' is missing or empty. No fallback allowed.", key)
	}
	return val
}

func MustLoad() *Config {
	// Nạp file .env nếu có (không báo lỗi nếu biến đã được inject trong container)
	_ = godotenv.Load()

	apiIDStr := mustGetEnv("TG_API_ID")
	apiID, err := strconv.Atoi(apiIDStr)
	if err != nil {
		log.Fatalf("FATAL: TG_API_ID '%s' is not a valid integer.", apiIDStr)
	}

	apiHash := mustGetEnv("TG_API_HASH")
	sessionString := mustGetEnv("TG_SESSION_STRING")

	channelIDStr := mustGetEnv("TG_CHANNEL_ID")
	channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
	if err != nil {
		log.Fatalf("FATAL: TG_CHANNEL_ID '%s' is not a valid int64.", channelIDStr)
	}

	mysqlURL := mustGetEnv("MYSQL_URL")
	jwtSecretStr := mustGetEnv("JWT_SECRET")
	aesKeyStr := mustGetEnv("AES_SECRET_KEY")

	if len(aesKeyStr) != 32 {
		log.Fatalf("FATAL: AES_SECRET_KEY must be exactly 32 bytes (got %d bytes).", len(aesKeyStr))
	}

	password := mustGetEnv("PASSWORD")
	port := mustGetEnv("PORT")

	chunkSize := 512 * 1024 // 512KB default MTProto chunk
	if csStr := strings.TrimSpace(os.Getenv("CHUNK_SIZE")); csStr != "" {
		if cs, err := strconv.Atoi(csStr); err == nil && cs > 0 {
			if cs <= 1024 { // nếu nhập dạng KB (ví dụ 512)
				chunkSize = cs * 1024
			} else {
				chunkSize = cs
			}
		}
	}

	return &Config{
		TgAPIID:         apiID,
		TgAPIHash:       apiHash,
		TgSessionString: sessionString,
		TgChannelID:     channelID,
		MySQLURL:        mysqlURL,
		JWTSecret:       []byte(jwtSecretStr),
		AESSecretKey:    []byte(aesKeyStr),
		Password:        password,
		Port:            port,
		ChunkSize:       chunkSize,
	}
}
