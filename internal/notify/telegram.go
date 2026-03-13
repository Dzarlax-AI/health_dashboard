package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Bot is a minimal Telegram bot client.
type Bot struct {
	token  string
	chatID string
}

func NewBot(token, chatID string) *Bot {
	return &Bot{token: token, chatID: chatID}
}

// Send sends an HTML-formatted message to the configured chat.
func (b *Bot) Send(text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", b.token)
	payload, _ := json.Marshal(map[string]string{
		"chat_id":    b.chatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API: status %d", resp.StatusCode)
	}
	return nil
}
