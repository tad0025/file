package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"videostream/internal/config"
)

type Client struct {
	client    *telegram.Client
	api       *tg.Client
	cfg       *config.Config
	peerMu    sync.RWMutex
	inputPeer tg.InputPeerClass
}

func CleanChannelID(id int64) int64 {
	if id < 0 {
		id = -id
	}
	idStr := strconv.FormatInt(id, 10)
	if strings.HasPrefix(idStr, "100") && len(idStr) > 3 {
		parsed, err := strconv.ParseInt(idStr[3:], 10, 64)
		if err == nil {
			return parsed
		}
	}
	return id
}

func NewClient(cfg *config.Config) (*Client, error) {
	storage, err := CreateTelethonStorage(cfg.TgSessionString)
	if err != nil {
		return nil, fmt.Errorf("CreateTelethonStorage: %w", err)
	}

	opts := telegram.Options{
		SessionStorage: storage,
	}

	client := telegram.NewClient(cfg.TgAPIID, cfg.TgAPIHash, opts)
	return &Client{
		client: client,
		api:    client.API(),
		cfg:    cfg,
	}, nil
}

func (c *Client) API() *tg.Client {
	return c.api
}

func (c *Client) Run(ctx context.Context, ready func(ctx context.Context) error) error {
	return c.client.Run(ctx, func(ctx context.Context) error {
		// Kiểm tra thông tin người dùng
		self, err := c.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			log.Printf("[Telegram] Warning getting self user: %v", err)
		} else if len(self) > 0 {
			if u, ok := self[0].(*tg.User); ok {
				log.Printf("[Telegram] Connected successfully as %s (@%s)", u.FirstName, u.Username)
			}
		}

		// Định vị Channel
		if err := c.findChannelPeer(ctx); err != nil {
			log.Printf("[Telegram] Warning resolving channel %d: %v", c.cfg.TgChannelID, err)
		}

		return ready(ctx)
	})
}

func (c *Client) findChannelPeer(ctx context.Context) error {
	targetID := CleanChannelID(c.cfg.TgChannelID)

	dialogs, err := c.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return fmt.Errorf("MessagesGetDialogs: %w", err)
	}

	var chats []tg.ChatClass
	switch d := dialogs.(type) {
	case *tg.MessagesDialogs:
		chats = d.Chats
	case *tg.MessagesDialogsSlice:
		chats = d.Chats
	}

	for _, ch := range chats {
		if channel, ok := ch.(*tg.Channel); ok {
			if channel.ID == targetID {
				c.peerMu.Lock()
				c.inputPeer = &tg.InputPeerChannel{
					ChannelID:  channel.ID,
					AccessHash: channel.AccessHash,
				}
				c.peerMu.Unlock()
				log.Printf("[Telegram] Found target channel: %s (ID: %d)", channel.Title, channel.ID)
				return nil
			}
		}
	}

	// Fallback nếu không thấy trong 100 dialogs đầu
	c.peerMu.Lock()
	c.inputPeer = &tg.InputPeerChannel{
		ChannelID: targetID,
	}
	c.peerMu.Unlock()
	return nil
}

func (c *Client) GetInputPeer() tg.InputPeerClass {
	c.peerMu.RLock()
	defer c.peerMu.RUnlock()
	return c.inputPeer
}
