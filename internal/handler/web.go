package handler

import (
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"videostream/internal/auth"
	"videostream/internal/database"
)

type WebHandler struct {
	db          *database.DB
	auth        *auth.AuthManager
	templateDir string
}

func NewWebHandler(db *database.DB, auth *auth.AuthManager, templateDir string) *WebHandler {
	return &WebHandler{
		db:          db,
		auth:        auth,
		templateDir: templateDir,
	}
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(bytes)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "B"
}

func (h *WebHandler) renderTemplate(w http.ResponseWriter, tmplName string, data interface{}) {
	tmplPath := filepath.Join(h.templateDir, tmplName)
	basePath := filepath.Join(h.templateDir, "base.html")

	funcMap := template.FuncMap{
		"formatBytes": formatBytes,
	}

	tmpl, err := template.New("base.html").Funcs(funcMap).ParseFiles(basePath, tmplPath)
	if err != nil {
		http.Error(w, "Template parse error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
		http.Error(w, "Template execution error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *WebHandler) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Nếu đã đăng nhập hợp lệ thì chuyển hướng về trang chủ
	if cookie, err := r.Cookie("auth_token"); err == nil && h.auth.ValidateToken(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	h.renderTemplate(w, "login.html", map[string]interface{}{
		"Title": "Đăng nhập Hệ thống",
	})
}

func (h *WebHandler) HandleIndexPage(w http.ResponseWriter, r *http.Request) {
	videos, err := h.db.GetAllVideos()
	if err != nil {
		http.Error(w, "Failed to query videos: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderTemplate(w, "index.html", map[string]interface{}{
		"Title":  "Thư viện Video",
		"Videos": videos,
	})
}

func (h *WebHandler) HandleWatchPage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/watch/")
	videoID, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		http.Error(w, "ID video không hợp lệ", http.StatusBadRequest)
		return
	}

	video, err := h.db.GetVideoWithQualities(videoID)
	if err != nil {
		http.Error(w, "Video không tồn tại", http.StatusNotFound)
		return
	}

	// Lấy JWT token để truyền vào video player nếu cần
	var token string
	if cookie, err := r.Cookie("auth_token"); err == nil {
		token = cookie.Value
	}

	h.renderTemplate(w, "watch.html", map[string]interface{}{
		"Title": "Xem: " + video.Title,
		"Video": video,
		"Token": token,
	})
}
