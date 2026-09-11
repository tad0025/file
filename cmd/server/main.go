package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"videostream/internal/auth"
	"videostream/internal/config"
	"videostream/internal/database"
	"videostream/internal/handler"
	"videostream/internal/telegram"
)

func main() {
	log.Println("==================================================")
	log.Println("TeleStream Monolith Streaming Server Starting...")
	log.Println("==================================================")

	// 1. Kiểm tra nghiêm ngặt biến môi trường (Fail-Fast, Zero-Fallback)
	cfg := config.MustLoad()
	log.Printf("[Config] Validated all environment variables successfully.")

	// 2. Kết nối MySQL và thực hiện Schema Migration tự động
	log.Printf("[Database] Connecting to MySQL (%s)...", cfg.MySQLURL)
	db, err := database.Connect(cfg.MySQLURL)
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to MySQL: %v", err)
	}
	defer db.Close()
	log.Println("[Database] MySQL connected and schema migrated successfully.")

	// 3. Khởi tạo Telegram MTProto Client
	log.Println("[Telegram] Initializing MTProto client from Telethon session string...")
	tgClient, err := telegram.NewClient(cfg)
	if err != nil {
		log.Fatalf("FATAL: Failed to initialize Telegram client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tgReady := make(chan struct{})

	// Chạy MTProto client nền trong goroutine
	go func() {
		err := tgClient.Run(ctx, func(readyCtx context.Context) error {
			close(tgReady)
			log.Println("[Telegram] MTProto client is connected and ready.")
			<-readyCtx.Done()
			return readyCtx.Err()
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("[Telegram] Warning: client run loop exited: %v", err)
		}
	}()

	// Đợi Telegram sẵn sàng (tối đa 30s)
	select {
	case <-tgReady:
	case <-time.After(30 * time.Second):
		log.Println("[Telegram] Warning: MTProto client connect timed out, continuing startup...")
	}

	// 4. Khởi tạo các module nghiệp vụ
	authMgr := auth.NewAuthManager(cfg)
	downloader := telegram.NewDownloader(tgClient)
	streamHandler := handler.NewStreamHandler(db, downloader, cfg)
	apiHandler := handler.NewAPIHandler(db, authMgr)
	webHandler := handler.NewWebHandler(db, authMgr, "web/templates")

	// 5. Cấu hình định tuyến HTTP
	mux := http.NewServeMux()

	// Static & Favicon
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, "web/static/favicon.ico")
	})
	mux.HandleFunc("/favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, "web/static/favicon.png")
	})

	// Endpoints công khai
	mux.HandleFunc("/login", webHandler.HandleLoginPage)
	mux.HandleFunc("/api/login", apiHandler.HandleLogin)
	mux.HandleFunc("/health", apiHandler.HandleHealth)

	// Endpoints bảo vệ
	mux.HandleFunc("/", webHandler.HandleIndexPage)
	mux.HandleFunc("/watch/", webHandler.HandleWatchPage)
	mux.HandleFunc("/api/logout", apiHandler.HandleLogout)
	mux.HandleFunc("/api/videos", apiHandler.HandleGetVideos)
	mux.HandleFunc("/api/videos/", apiHandler.HandleGetVideoDetail)
	mux.Handle("/api/stream/", streamHandler)

	// Áp dụng Auth Middleware
	rootHandler := authMgr.Middleware(mux)

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      rootHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Không giới hạn timeout cho video streaming
		IdleTimeout:  60 * time.Second,
	}

	// 6. Xử lý Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[Server] Listening on http://0.0.0.0:%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("FATAL: HTTP server ListenAndServe error: %v", err)
		}
	}()

	<-stop
	log.Println("[Server] Shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Server] Shutdown error: %v", err)
	}

	cancel()
	log.Println("[Server] Server stopped.")
}
