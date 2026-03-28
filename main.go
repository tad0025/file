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
	Sender   *websocket.Conn
	Receiver *websocket.Conn
	// Hàng đợi chứa tối đa 3 chunk trong RAM
	ChunkQueue chan []byte 
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
		rooms[tid] = &Room{
			ChunkQueue: make(chan []byte, 3), // Giới hạn 3 chunk
		}
	}
	room := rooms[tid]
	mapMu.Unlock()

	if role == "sender" {
		room.Sender = ws
		log.Printf("Sender joined: %s", tid)
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil { break }
			if mt == websocket.BinaryMessage {
				// Đẩy vào hàng đợi, sẽ bị chặn nếu đã đủ 3 chunk (Backpressure)
				room.ChunkQueue <- message 
			} else {
				// Relay Metadata (JSON) trực tiếp
				room.mu.Lock()
				if room.Receiver != nil { room.Receiver.WriteMessage(mt, message) }
				room.mu.Unlock()
			}
		}
	} else {
		room.Receiver = ws
		log.Printf("Receiver joined: %s", tid)
		// Luồng riêng để đẩy dữ liệu từ hàng đợi sang Receiver
		go func() {
			for msg := range room.ChunkQueue {
				room.Receiver.WriteMessage(websocket.BinaryMessage, msg)
			}
		}()
		for {
			// Nhận ACK từ Receiver và chuyển lại cho Sender
			mt, message, err := ws.ReadMessage()
			if err != nil { break }
			room.mu.Lock()
			if room.Sender != nil { room.Sender.WriteMessage(mt, message) }
			room.mu.Unlock()
		}
	}
}

func main() {
	http.Handle("/", http.FileServer(http.Dir("./public")))
	http.HandleFunc("/ws", handleConnections)
	port := os.Getenv("PORT")
	if port == "" { port = "8080" }
	http.ListenAndServe(":"+port, nil)
}