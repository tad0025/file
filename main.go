package main

import (
	"log" // Đã sử dụng để tránh lỗi build
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
	Metadata   []byte
	mu         sync.Mutex
}

var (
	rooms = make(map[string]*Room)
	mapMu sync.Mutex
)

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	role := r.URL.Query().Get("role")
	tid := r.URL.Query().Get("tid")

	mapMu.Lock()
	if _, ok := rooms[tid]; !ok {
		rooms[tid] = &Room{ChunkQueue: make(chan []byte, 10)} // Tăng lên 10 để mượt hơn
	}
	room := rooms[tid]
	mapMu.Unlock()

	// Ghi log để theo dõi trên Render
	log.Printf("Phòng %s: %s đã tham gia", tid, role)

	defer func() {
		room.mu.Lock()
		if role == "sender" {
			room.Sender = nil
		} else {
			room.Receiver = nil
		}

		if room.Sender == nil && room.Receiver == nil {
			mapMu.Lock()
			delete(rooms, tid)
			mapMu.Unlock()
		}
		room.mu.Unlock()
		ws.Close()
		log.Printf("Phòng %s: %s đã thoát", tid, role)
	}()

	if role == "sender" {
		room.Sender = ws
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if mt == websocket.BinaryMessage {
				room.ChunkQueue <- message
			} else {
				room.mu.Lock()
				room.Metadata = message
				if room.Receiver != nil {
					room.Receiver.WriteMessage(websocket.TextMessage, message)
				}
				room.mu.Unlock()
			}
		}
	} else {
		room.Receiver = ws
		room.mu.Lock()
		if room.Metadata != nil {
			room.Receiver.WriteMessage(websocket.TextMessage, room.Metadata)
		}
		room.mu.Unlock()

		go func() {
			for msg := range room.ChunkQueue {
				if room.Receiver != nil {
					if err := room.Receiver.WriteMessage(websocket.BinaryMessage, msg); err != nil {
						return
					}
				}
			}
		}()

		for {
			mt, message, err := ws.ReadMessage()
			if err != nil {
				break
			}
			room.mu.Lock()
			if room.Sender != nil {
				room.Sender.WriteMessage(mt, message)
			}
			room.mu.Unlock()
		}
	}
}

func main() {
	http.Handle("/", http.FileServer(http.Dir("./public")))
	http.HandleFunc("/ws", handleConnections)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Server đang chạy tại port: " + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}