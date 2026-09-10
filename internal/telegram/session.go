package telegram

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"strings"

	"github.com/gotd/td/session"
)

// CreateTelethonStorage giải mã chuỗi Telethon v1 StringSession và đóng gói thành session.Storage cho gotd
func CreateTelethonStorage(sessionStr string) (session.Storage, error) {
	sessionStr = strings.TrimSpace(sessionStr)
	if len(sessionStr) == 0 {
		return nil, fmt.Errorf("empty session string")
	}

	if sessionStr[0] != '1' {
		return nil, fmt.Errorf("unsupported session version: only Telethon v1 (starts with '1') is supported")
	}

	rawB64 := sessionStr[1:]
	if m := len(rawB64) % 4; m != 0 {
		rawB64 += strings.Repeat("=", 4-m)
	}

	data, err := base64.URLEncoding.DecodeString(rawB64)
	if err != nil {
		return nil, fmt.Errorf("failed to base64 decode session string: %w", err)
	}

	if len(data) < 263 {
		return nil, fmt.Errorf("session payload too short: %d bytes (expected >= 263)", len(data))
	}

	dcID := int(data[0])
	var ipStr string
	var port uint16
	var authKey []byte

	if len(data) == 263 { // IPv4
		ip := net.IP(data[1:5])
		ipStr = ip.String()
		port = binary.BigEndian.Uint16(data[5:7])
		authKey = data[7 : 7+256]
	} else if len(data) == 275 { // IPv6
		ip := net.IP(data[1:17])
		ipStr = ip.String()
		port = binary.BigEndian.Uint16(data[17:19])
		authKey = data[19 : 19+256]
	} else {
		ip := net.IP(data[1:5])
		ipStr = ip.String()
		port = binary.BigEndian.Uint16(data[5:7])
		authKey = data[7 : 7+256]
	}

	// AuthKeyID trong MTProto là 8 bytes cuối của SHA1(auth_key)
	h := sha1.Sum(authKey)
	authKeyID := make([]byte, 8)
	copy(authKeyID, h[12:20])

	sessionData := session.Data{
		DC:        dcID,
		Addr:      fmt.Sprintf("%s:%d", ipStr, port),
		AuthKey:   authKey,
		AuthKeyID: authKeyID,
	}

	memStorage := &session.StorageMemory{}
	loader := session.Loader{Storage: memStorage}
	if err := loader.Save(context.Background(), &sessionData); err != nil {
		return nil, fmt.Errorf("failed to save session into memory storage: %w", err)
	}

	return memStorage, nil
}
