package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var errPeerClosed = errors.New("peer đã đóng")

type outboundMessage struct {
	mt      int
	payload []byte
}

type Peer struct {
	Conn      *websocket.Conn
	sendQueue chan outboundMessage
	done      chan struct{}
	closeOnce sync.Once
}

func newPeer(conn *websocket.Conn) *Peer {
	p := &Peer{
		Conn:      conn,
		sendQueue: make(chan outboundMessage, 64),
		done:      make(chan struct{}),
	}
	go p.writePump()
	return p
}

func (p *Peer) writePump() {
	for {
		select {
		case <-p.done:
			return
		case msg := <-p.sendQueue:
			if err := p.Conn.WriteMessage(msg.mt, msg.payload); err != nil {
				p.Close()
				return
			}
		}
	}
}

func (p *Peer) Send(mt int, payload []byte) error {
	payloadCopy := append([]byte(nil), payload...)

	select {
	case <-p.done:
		return errPeerClosed
	case p.sendQueue <- outboundMessage{mt: mt, payload: payloadCopy}:
		return nil
	}
}

func (p *Peer) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.Conn.Close()
	})
}

type Room struct {
	Sender     *Peer
	Receiver   *Peer
	Metadata   []byte
	Hash       []byte
	ChunkQueue chan []byte

	mu     sync.Mutex
	cond   *sync.Cond
	done   chan struct{}
	closed bool
}

var (
	rooms = make(map[string]*Room)
	mapMu sync.Mutex
)

type SignalMessage struct {
	Type string `json:"type"`
}

func newRoom() *Room {
	room := &Room{
		ChunkQueue: make(chan []byte, 3), // Server giữ tối đa 3 chunk để tạo backpressure ổn định.
		done:       make(chan struct{}),
	}
	room.cond = sync.NewCond(&room.mu)
	go room.forwardChunks()
	return room
}

func (r *Room) forwardChunks() {
	for {
		select {
		case <-r.done:
			return
		case chunk, ok := <-r.ChunkQueue:
			if !ok {
				return
			}

			for {
				receiver := r.waitReceiver()
				if receiver == nil {
					return
				}

				if err := receiver.Send(websocket.BinaryMessage, chunk); err != nil {
					log.Printf("Receiver send lỗi, chờ receiver mới: %v", err)
					r.mu.Lock()
					if r.Receiver == receiver {
						r.Receiver = nil
					}
					r.mu.Unlock()
					continue
				}
				break
			}
		}
	}
}

func (r *Room) waitReceiver() *Peer {
	r.mu.Lock()
	defer r.mu.Unlock()

	for !r.closed && r.Receiver == nil {
		r.cond.Wait()
	}
	if r.closed {
		return nil
	}
	return r.Receiver
}

func (r *Room) setSender(peer *Peer) {
	r.mu.Lock()
	r.Sender = peer
	r.mu.Unlock()
}

func (r *Room) setReceiver(peer *Peer) {
	var meta []byte
	var hash []byte

	r.mu.Lock()
	r.Receiver = peer
	if len(r.Metadata) > 0 {
		meta = append([]byte(nil), r.Metadata...)
	}
	if len(r.Hash) > 0 {
		hash = append([]byte(nil), r.Hash...)
	}
	r.cond.Broadcast()
	r.mu.Unlock()

	if len(meta) > 0 {
		if err := peer.Send(websocket.TextMessage, meta); err != nil {
			log.Printf("Gửi metadata cho receiver lỗi: %v", err)
		}
	}
	if len(hash) > 0 {
		if err := peer.Send(websocket.TextMessage, hash); err != nil {
			log.Printf("Gửi hash cho receiver lỗi: %v", err)
		}
	}
}

func (r *Room) setMetadata(metadata []byte) {
	r.mu.Lock()
	r.Metadata = append([]byte(nil), metadata...)
	r.mu.Unlock()
}

func (r *Room) setHash(hash []byte) {
	r.mu.Lock()
	r.Hash = append([]byte(nil), hash...)
	r.mu.Unlock()
}

func (r *Room) enqueueChunk(chunk []byte) bool {
	chunkCopy := append([]byte(nil), chunk...)
	select {
	case <-r.done:
		return false
	case r.ChunkQueue <- chunkCopy:
		return true
	}
}

func (r *Room) forwardToSender(mt int, payload []byte) error {
	r.mu.Lock()
	sender := r.Sender
	r.mu.Unlock()

	if sender == nil {
		return errors.New("sender chưa kết nối")
	}
	return sender.Send(mt, payload)
}

func (r *Room) forwardToReceiver(mt int, payload []byte) error {
	r.mu.Lock()
	receiver := r.Receiver
	r.mu.Unlock()

	if receiver == nil {
		return errors.New("receiver chưa kết nối")
	}
	return receiver.Send(mt, payload)
}

func (r *Room) detach(role string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if role == "sender" {
		r.Sender = nil
	} else {
		r.Receiver = nil
	}

	r.cond.Broadcast()

	if r.Sender == nil && r.Receiver == nil && !r.closed {
		r.closed = true
		close(r.done)
		close(r.ChunkQueue)
		return true
	}
	return false
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	role := r.URL.Query().Get("role")
	tid := r.URL.Query().Get("tid")
	if role != "sender" && role != "receiver" {
		ws.Close()
		return
	}
	if tid == "" {
		ws.Close()
		return
	}

	mapMu.Lock()
	if _, ok := rooms[tid]; !ok {
		rooms[tid] = newRoom()
	}
	room := rooms[tid]
	mapMu.Unlock()

	peer := newPeer(ws)
	log.Printf("Phòng %s: %s đã tham gia", tid, role)

	defer func() {
		shouldDelete := room.detach(role)
		if shouldDelete {
			mapMu.Lock()
			delete(rooms, tid)
			mapMu.Unlock()
		}
		peer.Close()
		log.Printf("Phòng %s: %s đã thoát", tid, role)
	}()

	if role == "sender" {
		room.setSender(peer)
		for {
			mt, message, err := ws.ReadMessage()
			if err != nil {
				break
			}

			switch mt {
			case websocket.BinaryMessage:
				if !room.enqueueChunk(message) {
					return
				}
			case websocket.TextMessage:
				switch parseSignalType(message) {
				case "meta":
					room.setMetadata(message)
				case "hash":
					room.setHash(message)
				}
				_ = room.forwardToReceiver(websocket.TextMessage, message)
			}
		}
		return
	}

	room.setReceiver(peer)
	for {
		mt, message, err := ws.ReadMessage()
		if err != nil {
			break
		}
		_ = room.forwardToSender(mt, message)
	}
}

func parseSignalType(payload []byte) string {
	var signal SignalMessage
	if err := json.Unmarshal(payload, &signal); err != nil {
		return ""
	}
	return signal.Type
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
