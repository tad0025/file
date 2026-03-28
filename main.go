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

// Quản lý các kết nối trong một phiên truyền file
type Room struct {
	Sender   *websocket.Conn
	Receiver *websocket.Conn
	mu       sync.Mutex
}

var (
	rooms = make(map[string]*Room)
	mapMu sync.Mutex
)

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Lấy thông tin từ query: ?role=sender&tid=123
	role := r.URL.Query().Get("role")
	tid := r.URL.Query().Get("tid")

	mapMu.Lock()
	if _, ok := rooms[tid]; !ok {
		rooms[tid] = &Room{}
	}
	room := rooms[tid]
	mapMu.Unlock()

	room.mu.Lock()
	if role == "sender" {
		room.Sender = ws
	} else {
		room.Receiver = ws
	}
	room.mu.Unlock()

	log.Printf("New %s joined room: %s", role, tid)

	// Xử lý chuyển tiếp dữ liệu
	for {
		mt, message, err := ws.ReadMessage()
		if err != nil {
			break
		}

		// Nếu là sender gửi, đẩy ngay sang receiver và ngược lại (cho ACK)
		room.mu.Lock()
		var target *websocket.Conn
		if role == "sender" {
			target = room.Receiver
		} else {
			target = room.Sender
		}

		if target != nil {
			target.WriteMessage(mt, message)
		}
		room.mu.Unlock()
	}

	// Dọn dẹp khi ngắt kết nối
	mapMu.Lock()
	delete(rooms, tid)
	mapMu.Unlock()
}

func main() {
	// Phục vụ giao diện tĩnh
	http.Handle("/", http.FileServer(http.Dir("./public")))
	// Endpoint WebSocket
	http.HandleFunc("/ws", handleConnections)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Println("Server đang chạy tại port: " + port)
	http.ListenAndServe(":"+port, nil)
}