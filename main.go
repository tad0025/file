package main

import (
	"log"
	"net/http"
	"os"
	"sync"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Room struct {
	Sender     *websocket.Conn
	Receiver   *websocket.Conn
	ChunkQueue chan []byte
	Metadata   []byte // Lưu trữ Metadata (tên file, size, hash)
	mu         sync.Mutex
}

var (
	rooms = make(map[string]*Room)
	mapMu sync.Mutex
)

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }

	role := r.URL.Query().Get("role")
	tid := r.URL.Query().Get("tid")

	mapMu.Lock()
	if _, ok := rooms[tid]; !ok {
		rooms[tid] = &Room{ ChunkQueue: make(chan []byte, 3) }
	}
	room := rooms[tid]
	mapMu.Unlock()

	defer func() {
		mapMu.Lock()
		room, ok := rooms[tid]
		if ok {
			room.mu.Lock()
			if role == "sender" { room.Sender = nil }
			if role == "receiver" { room.Receiver = nil }
			// Chỉ xóa khi cả 2 đều đã thoát
			if room.Sender == nil && room.Receiver == nil {
				close(room.ChunkQueue) // Đóng channel để giải phóng tài nguyên
				delete(rooms, tid)
			}
			room.mu.Unlock()
		}
		mapMu.Unlock()
		ws.Close()
	}()

	if role == "sender" {
		room.Sender = ws
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil { break }
			if mt == websocket.BinaryMessage {
				room.ChunkQueue <- message
			} else {
				// Lưu Metadata vào phòng để người nhận vào sau vẫn thấy
				room.mu.Lock()
				room.Metadata = message
				if room.Receiver != nil {
					room.Receiver.WriteMessage(mt, message)
				}
				room.mu.Unlock()
			}
		}
	} else {
		room.Receiver = ws
		// Nếu người gửi đã gửi Metadata trước đó, gửi ngay cho người nhận vừa vào
		room.mu.Lock()
		if room.Metadata != nil {
			room.Receiver.WriteMessage(websocket.TextMessage, room.Metadata)
		}
		room.mu.Unlock()

		go func() {
			for msg := range room.ChunkQueue {
				room.Receiver.WriteMessage(websocket.BinaryMessage, msg)
			}
		}()
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil { break }
			room.mu.Lock()
			if room.Sender != nil { room.Sender.WriteMessage(mt, message) }
			room.mu.Unlock()
		}
	}
    // Lưu ý: Chỉ nên xóa room khi cả hai cùng thoát để tránh mất dữ liệu giữa chừng
}

func main() {
	http.Handle("/", http.FileServer(http.Dir("./public")))
	http.HandleFunc("/ws", handleConnections)
	port := os.Getenv("PORT")
	if port == "" { port = "8080" }

	// Thêm dòng này để sử dụng thư viện log
	log.Println("Server đang chạy tại port: " + port)

	http.ListenAndServe(":"+port, nil)
}