package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"max/internal/app"
	"max/internal/platform"
)

const defaultTelegramAPI = "https://api.telegram.org"

// Config configures the optional notify-only Telegram adapter (Spec 8.6 & D8: zero approval buttons).
type Config struct {
	APIBase   string
	BotToken  string
	OwnerIDs  []int64
	PublicURL string
	Clock     app.Clock
	Logger    *slog.Logger
}

// Update represents a minimal incoming Telegram message envelope checked against OwnerIDs.
type Update struct {
	FromUserID int64
	Text       string
}

// Notifier implements app.Notifier over Telegram Bot HTTP sendMessage (notify-only, never interactive buttons).
type Notifier struct {
	cfg        Config
	httpClient *http.Client

	mu             sync.Mutex
	lastStrayLogAt time.Time
}

var _ app.Notifier = (*Notifier)(nil)

// New constructs a Telegram Notifier (disabled no-op when BotToken or OwnerIDs are empty).
func New(cfg Config) *Notifier {
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = defaultTelegramAPI
	}
	if cfg.Clock == nil {
		cfg.Clock = app.SystemClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Notifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether both MAX_TELEGRAM_BOT_TOKEN and at least one MAX_TELEGRAM_OWNER_IDS are configured.
func (n *Notifier) Enabled() bool {
	return strings.TrimSpace(n.cfg.BotToken) != "" && len(n.cfg.OwnerIDs) > 0
}

// Notify sends a plain-text redacted alert with a Web UI link (never including reply_markup or approval buttons).
func (n *Notifier) Notify(ctx context.Context, msg app.Notification) error {
	if !n.Enabled() {
		return nil
	}
	link := strings.TrimSpace(msg.LinkURL)
	if link == "" && strings.TrimSpace(n.cfg.PublicURL) != "" {
		link = strings.TrimRight(n.cfg.PublicURL, "/") + "/inbox"
	}

	var sb strings.Builder
	sb.WriteString("[Max] ")
	sb.WriteString(platform.Redact(msg.Title))
	if strings.TrimSpace(msg.Body) != "" {
		sb.WriteString("\n")
		sb.WriteString(platform.Redact(msg.Body))
	}
	if link != "" {
		sb.WriteString("\nXem & xử lý trên Web UI: ")
		sb.WriteString(link)
	}
	text := sb.String()

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(n.cfg.APIBase, "/"), n.cfg.BotToken)
	for _, ownerID := range n.cfg.OwnerIDs {
		// Strictly send chat_id + text + disable_web_page_preview ONLY (never reply_markup / inline_keyboard).
		payload, _ := json.Marshal(map[string]any{
			"chat_id":                  ownerID,
			"text":                     text,
			"disable_web_page_preview": true,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("telegram sendMessage: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("telegram sendMessage HTTP %d", resp.StatusCode)
		}
	}
	return nil
}

// HandleIncomingUpdate drops messages from non-owner user IDs and rate-limits warning logs to <= 1/minute.
func (n *Notifier) HandleIncomingUpdate(u Update) bool {
	if !n.Enabled() {
		return false
	}
	if slices.Contains(n.cfg.OwnerIDs, u.FromUserID) {
		return true
	}
	now := n.cfg.Clock.Now()
	n.mu.Lock()
	shouldLog := n.lastStrayLogAt.IsZero() || now.Sub(n.lastStrayLogAt) >= time.Minute
	if shouldLog {
		n.lastStrayLogAt = now
	}
	n.mu.Unlock()

	if shouldLog {
		n.cfg.Logger.Warn("ignored telegram message from non-owner user", "user_id", u.FromUserID)
	}
	return false
}
