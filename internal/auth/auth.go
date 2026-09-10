package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"videostream/internal/config"
)

type Claims struct {
	Authorized bool `json:"authorized"`
	jwt.RegisteredClaims
}

type AuthManager struct {
	cfg *config.Config
}

func NewAuthManager(cfg *config.Config) *AuthManager {
	return &AuthManager{cfg: cfg}
}

// VerifyPassword kiểm tra mật khẩu bằng constant time compare chống tấn công timing
func (a *AuthManager) VerifyPassword(input string) bool {
	return subtle.ConstantTimeCompare([]byte(input), []byte(a.cfg.Password)) == 1
}

// GenerateToken sinh JWT token có hạn 7 ngày
func (a *AuthManager) GenerateToken() (string, error) {
	claims := Claims{
		Authorized: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.cfg.JWTSecret)
}

// ValidateToken xác thực JWT token
func (a *AuthManager) ValidateToken(tokenStr string) bool {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		return a.cfg.JWTSecret, nil
	})
	if err != nil || !token.Valid {
		return false
	}
	return true
}

// Middleware bảo vệ các endpoint yêu cầu đăng nhập
func (a *AuthManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bỏ qua các endpoint công khai
		path := r.URL.Path
		if path == "/health" || path == "/login" || path == "/api/login" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		var tokenStr string

		// 1. Kiểm tra Cookie
		if cookie, err := r.Cookie("auth_token"); err == nil && cookie.Value != "" {
			tokenStr = cookie.Value
		}

		// 2. Kiểm tra Header Authorization
		if tokenStr == "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		// 3. Kiểm tra Query param (tiện cho video player stream)
		if tokenStr == "" {
			tokenStr = r.URL.Query().Get("token")
		}

		if tokenStr == "" || !a.ValidateToken(tokenStr) {
			if strings.HasPrefix(path, "/api/") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}
