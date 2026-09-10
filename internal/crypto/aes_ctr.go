package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// GenerateIV tạo ngẫu nhiên 16 bytes IV và trả về dạng []byte cùng chuỗi hex 32 ký tự
func GenerateIV() ([]byte, string, error) {
	iv := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, "", err
	}
	return iv, hex.EncodeToString(iv), nil
}

// AddOffsetToIV tăng giá trị IV thêm một số lượng khối (blocks) 16 bytes
func AddOffsetToIV(iv []byte, blocks uint64) []byte {
	res := make([]byte, 16)
	copy(res, iv)
	carry := blocks
	for i := 15; i >= 0 && carry > 0; i-- {
		total := uint64(res[i]) + (carry & 0xFF)
		res[i] = byte(total)
		carry = (carry >> 8) + (total >> 8)
	}
	return res
}

// NewSeekableCTR khởi tạo cipher.Stream cho AES-256-CTR tua trực tiếp tới localOffset
func NewSeekableCTR(key, iv []byte, localOffset int64) (cipher.Stream, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	blocks := uint64(localOffset / 16)
	offsetIV := AddOffsetToIV(iv, blocks)

	stream := cipher.NewCTR(block, offsetIV)

	// Bỏ qua phần dư nếu offset không chia hết cho 16 bytes
	discardBytes := localOffset % 16
	if discardBytes > 0 {
		discardBuf := make([]byte, discardBytes)
		stream.XORKeyStream(discardBuf, discardBuf)
	}

	return stream, nil
}

// EncryptFile mã hóa một file local bằng AES-256-CTR và ghi ra file đích
func EncryptFile(srcPath, dstPath string, key, iv []byte) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	stream := cipher.NewCTR(block, iv)
	writer := &cipher.StreamWriter{S: stream, W: dst}

	buf := make([]byte, 512*1024)
	if _, err := io.CopyBuffer(writer, src, buf); err != nil {
		return err
	}

	return nil
}
