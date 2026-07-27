package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	discordWebhookEnv     = "DISCORD_WEBHOOK_URL"
	discordDefaultStatus  = "Ticket Reserved in Cart"
	discordMaxRetries     = 3
	discordWebhookTimeout = 15 * time.Second
)

// CartInfo holds ticket cart details shown in the Discord embed.
type CartInfo struct {
	EventName     string
	EventID       string
	Venue         string
	EventDateTime string
	Category      string
	Quantity      int
	Price         string
	SeatInfo      string // seat / section when available
	Status        string // e.g. "Ticket Reserved in Cart"
	Timestamp     time.Time
}

// DiscordNotifier posts cart/reserve alerts to a Discord webhook.
type DiscordNotifier struct {
	WebhookURL string
	HTTPClient *http.Client
	MaxRetries int
}

// NewDiscordNotifierFromEnv builds a notifier using DISCORD_WEBHOOK_URL from the environment.
func NewDiscordNotifierFromEnv() (*DiscordNotifier, error) {
	url := strings.TrimSpace(os.Getenv(discordWebhookEnv))
	if url == "" {
		return nil, fmt.Errorf("missing %s in environment (see .env.example)", discordWebhookEnv)
	}
	return NewDiscordNotifier(url), nil
}

// NewDiscordNotifier returns a notifier for the given Discord webhook URL.
func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	return &DiscordNotifier{
		WebhookURL: strings.TrimSpace(webhookURL),
		HTTPClient: &http.Client{Timeout: discordWebhookTimeout},
		MaxRetries: discordMaxRetries,
	}
}

// NotifyCartReserved sends an embed after a successful reserve / add-to-cart.
func (n *DiscordNotifier) NotifyCartReserved(info CartInfo) error {
	if n == nil || n.WebhookURL == "" {
		return fmt.Errorf("discord webhook URL is not configured")
	}
	payload, err := buildDiscordCartPayload(info)
	if err != nil {
		return err
	}
	return n.postWithRetry(payload)
}

// NotifyCartReservedFromEnv loads DISCORD_WEBHOOK_URL and sends a cart reserved notification.
func NotifyCartReservedFromEnv(info CartInfo) error {
	n, err := NewDiscordNotifierFromEnv()
	if err != nil {
		return err
	}
	return n.NotifyCartReserved(info)
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbedFooter struct {
	Text string `json:"text"`
}

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields"`
	Timestamp   string              `json:"timestamp"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
}

type discordWebhookPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds"`
}

func buildDiscordCartPayload(info CartInfo) ([]byte, error) {
	status := info.Status
	if status == "" {
		status = discordDefaultStatus
	}
	ts := info.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	fields := make([]discordEmbedField, 0, 10)
	fields = append(fields, discordField("Event", discordOrDash(info.EventName), false))
	if strings.TrimSpace(info.EventID) != "" {
		fields = append(fields, discordField("Event ID", info.EventID, true))
	}
	fields = append(fields,
		discordField("Venue", discordOrDash(info.Venue), true),
		discordField("Date / Time", discordOrDash(info.EventDateTime), true),
		discordField("Category", discordOrDash(info.Category), true),
		discordField("Quantity", discordFormatQty(info.Quantity), true),
		discordField("Price", discordOrDash(info.Price), true),
	)
	if strings.TrimSpace(info.SeatInfo) != "" {
		fields = append(fields, discordField("Seat / Section", info.SeatInfo, false))
	}
	fields = append(fields,
		discordField("Cart Status", status, false),
		discordField("Timestamp", ts.Local().Format("2006-01-02 15:04:05 MST"), false),
	)

	payload := discordWebhookPayload{
		Embeds: []discordEmbed{{
			Title:       "Ticket Reserved",
			Description: status,
			Color:       0x57F287,
			Fields:      fields,
			Timestamp:   ts.UTC().Format(time.RFC3339),
			Footer:      &discordEmbedFooter{Text: "LA28 Ticket Monitor"},
		}},
	}
	return json.Marshal(payload)
}

func discordField(name, value string, inline bool) discordEmbedField {
	if strings.TrimSpace(value) == "" {
		value = "—"
	}
	if len(value) > 1024 {
		value = value[:1021] + "..."
	}
	return discordEmbedField{Name: name, Value: value, Inline: inline}
}

func discordOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func discordFormatQty(n int) string {
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func (n *DiscordNotifier) postWithRetry(body []byte) error {
	retries := n.MaxRetries
	if retries < 1 {
		retries = 1
	}
	client := n.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: discordWebhookTimeout}
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		err := n.postOnce(client, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == retries {
			break
		}
		wait := time.Duration(attempt*attempt) * time.Second
		if retryable, ra := discordAsRetryAfter(err); retryable && ra > 0 {
			wait = ra
		}
		time.Sleep(wait)
	}
	return fmt.Errorf("discord webhook failed after %d attempts: %w", retries, lastErr)
}

type discordRetryAfterError struct {
	statusCode int
	retryAfter time.Duration
	body       string
}

func (e *discordRetryAfterError) Error() string {
	return fmt.Sprintf("discord webhook HTTP %d: %s", e.statusCode, e.body)
}

func discordAsRetryAfter(err error) (bool, time.Duration) {
	if e, ok := err.(*discordRetryAfterError); ok {
		return true, e.retryAfter
	}
	return false, 0
}

func (n *DiscordNotifier) postOnce(client *http.Client, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "la28-monitor-discord-notify/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("discord webhook request failed: %w", discordRedactURLErr(err))
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	preview := strings.TrimSpace(string(respBody))

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		ra := discordParseRetryAfter(resp.Header.Get("Retry-After"))
		return &discordRetryAfterError{statusCode: resp.StatusCode, retryAfter: ra, body: preview}
	}
	return fmt.Errorf("discord webhook HTTP %d: %s", resp.StatusCode, preview)
}

func discordRedactURLErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if i := strings.Index(msg, "/api/webhooks/"); i >= 0 {
		prefix := msg[:i]
		rest := msg[i:]
		if j := strings.IndexAny(rest, "\": "); j > 0 {
			rest = rest[j:]
		} else {
			rest = ""
		}
		return fmt.Errorf("%s/api/webhooks/<redacted>%s", prefix, rest)
	}
	return err
}

func discordParseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var secs float64
	if _, err := fmt.Sscanf(v, "%f", &secs); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
