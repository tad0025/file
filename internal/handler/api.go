package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"videostream/internal/auth"
	"videostream/internal/database"
)

type APIHandler struct {
	db   *database.DB
	auth *auth.AuthManager
}

func NewAPIHandler(db *database.DB, auth *auth.AuthManager) *APIHandler {
	return &APIHandler{
		db:   db,
		auth: auth,
	}
}

type LoginRequest struct {
	Password string `json:"password"`
}

func (h *APIHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Thử đọc từ form data
		req.Password = r.FormValue("password")
	}

	if !h.auth.VerifyPassword(req.Password) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Mật khẩu không chính xác"})
		return
	}

	token, err := h.auth.GenerateToken()
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// Đặt Cookie HTTP-only
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(7 * 24 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"token":  token,
	})
}

func (h *APIHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *APIHandler) HandleGetVideos(w http.ResponseWriter, r *http.Request) {
	videos, err := h.db.GetAllVideos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(videos)
}

func (h *APIHandler) HandleGetVideoDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/videos/")
	videoID, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "Invalid video ID", http.StatusBadRequest)
		return
	}

	video, err := h.db.GetVideoWithQualities(videoID)
	if err != nil {
		http.Error(w, "Video not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(video)
}

func (h *APIHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	mysqlErr := h.db.Ping()
	status := "ok"
	if mysqlErr != nil {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":             status,
		"mysql_connected":    mysqlErr == nil,
		"telegram_connected": true,
		"timestamp":          time.Now().Format(time.RFC3339),
	})
}
