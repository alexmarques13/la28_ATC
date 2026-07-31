package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	discordBotTokenEnv     = "DISCORD_BOT_TOKEN"
	discordChannelEnv      = "DISCORD_CHANNEL_ID"
	openCartCustomIDPrefix = "opencart:"
)

type cartOpenSession struct {
	Client  *Client
	CartURL string
	Email   string
	Created time.Time
}

var (
	cartSessionsMu  sync.Mutex
	cartSessions    = map[string]*cartOpenSession{}
	discordSessMu   sync.Mutex
	discordSess     *discordgo.Session
	discordStartErr error
	discordStarted  bool
)

func registerCartSession(client *Client, cartURL, email string) string {
	id := randomSessionID()
	if cartURL == "" {
		cartURL = baseURL + "/shoppingCart/?affiliate=28V"
	}
	cartSessionsMu.Lock()
	cartSessions[id] = &cartOpenSession{
		Client:  client,
		CartURL: cartURL,
		Email:   email,
		Created: time.Now().UTC(),
	}
	cartSessionsMu.Unlock()
	return id
}

func lookupCartSession(id string) *cartOpenSession {
	cartSessionsMu.Lock()
	defer cartSessionsMu.Unlock()
	return cartSessions[id]
}

func unregisterCartSession(id string) {
	cartSessionsMu.Lock()
	delete(cartSessions, id)
	cartSessionsMu.Unlock()
}

func randomSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func discordBotTokenConfigured() bool {
	return strings.TrimSpace(os.Getenv(discordBotTokenEnv)) != ""
}

func ensureDiscordBot() error {
	discordSessMu.Lock()
	defer discordSessMu.Unlock()
	if discordStarted {
		return discordStartErr
	}
	discordStarted = true

	token := strings.TrimSpace(os.Getenv(discordBotTokenEnv))
	if token == "" {
		discordStartErr = fmt.Errorf("missing %s (required for Open Cart button)", discordBotTokenEnv)
		return discordStartErr
	}

	s, err := discordgo.New("Bot " + token)
	if err != nil {
		discordStartErr = err
		return err
	}
	s.AddHandler(handleDiscordInteraction)

	if err := s.Open(); err != nil {
		discordStartErr = fmt.Errorf("discord gateway open: %w", err)
		return discordStartErr
	}
	discordSess = s
	fmt.Println("[discord] bot connected — Open Cart clicks will open Chrome")
	return nil
}

func handleDiscordInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	data := i.MessageComponentData()
	if !strings.HasPrefix(data.CustomID, openCartCustomIDPrefix) {
		return
	}
	sid := strings.TrimPrefix(data.CustomID, openCartCustomIDPrefix)

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	sess := lookupCartSession(sid)
	if sess == nil || sess.Client == nil {
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: "Cart session expired or the monitor was restarted. Re-run ATC.",
			Flags:   discordgo.MessageFlagsEphemeral,
		})
		return
	}

	fmt.Printf("[discord] Open Cart clicked (session=%s email=%s) — opening Chrome; bot stays online\n", sid, sess.Email)
	go func() {
		err := sess.Client.OpenCartInBrowser(sess.CartURL)
		msg := "Chrome opened with your reserved cart. Monitor is still running."
		if err != nil {
			msg = fmt.Sprintf("Failed to open cart: %v", err)
			fmt.Printf("[discord] open cart failed: %v\n", err)
		} else {
			fmt.Println("[discord] Chrome launched — bot still listening for more clicks")
		}
		_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		})
	}()
}

func sendCartDiscordWithButton(info CartInfo, sessionID string) error {
	if err := ensureDiscordBot(); err != nil {
		return err
	}
	discordSessMu.Lock()
	s := discordSess
	discordSessMu.Unlock()
	if s == nil {
		return fmt.Errorf("discord session not ready")
	}

	channelID, err := resolveDiscordChannelID()
	if err != nil {
		return err
	}

	embed := cartInfoToDiscordEmbed(info)
	_, err = s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed},
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Open Cart",
						Style:    discordgo.PrimaryButton, // style 1 — NOT LinkButton (5)
						CustomID: openCartCustomIDPrefix + sessionID,
						// URL must stay empty — a URL forces a link button.
					},
				},
			},
		},
	})
	return err
}

func cartInfoToDiscordEmbed(info CartInfo) *discordgo.MessageEmbed {
	status := info.Status
	if status == "" {
		status = discordDefaultStatus
	}
	ts := info.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return &discordgo.MessageEmbed{
		Title:       "Ticket Reserved",
		Description: status,
		Color:       0x57F287,
		Timestamp:   ts.UTC().Format(time.RFC3339),
		Footer:      &discordgo.MessageEmbedFooter{Text: "LA28 Ticket Monitor"},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Event", Value: discordOrDash(info.EventName), Inline: false},
			{Name: "Venue", Value: discordOrDash(info.Venue), Inline: true},
			{Name: "Date / Time", Value: discordOrDash(info.EventDateTime), Inline: false},
			{Name: "Category", Value: discordOrDash(info.Category), Inline: true},
			{Name: "Quantity", Value: discordFormatQty(info.Quantity), Inline: true},
			{Name: "Ticket Price", Value: discordOrDash(info.TicketPrice), Inline: true},
			{Name: "Total", Value: discordOrDash(info.TotalPrice), Inline: true},
		},
	}
}

func resolveDiscordChannelID() (string, error) {
	if id := strings.TrimSpace(os.Getenv(discordChannelEnv)); id != "" {
		return id, nil
	}
	webhookURL := strings.TrimSpace(os.Getenv(discordWebhookEnv))
	if webhookURL == "" {
		return "", fmt.Errorf("set %s or %s", discordChannelEnv, discordWebhookEnv)
	}
	client := &http.Client{Timeout: discordWebhookTimeout}
	resp, err := client.Get(webhookURL)
	if err != nil {
		return "", fmt.Errorf("resolve channel from webhook: %w", discordRedactURLErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook lookup HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var meta struct {
		ChannelID string `json:"channel_id"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return "", err
	}
	if meta.ChannelID == "" {
		return "", fmt.Errorf("webhook response missing channel_id")
	}
	return meta.ChannelID, nil
}

// notifyCartReserved posts the Discord alert with an Open Cart button and keeps
// the process alive so a click can open Chrome with ATC cookies.
// Prefer a normal Primary button when DISCORD_BOT_TOKEN is set; otherwise attach
// an Open Cart button that hits the local cart opener (webhook link component).
func notifyCartReserved(email string, basket *BasketResult, client *Client) (keepAlive bool) {
	if basket == nil {
		return false
	}
	info := cartInfoFromBasket(email, basket)
	cartURL := info.CartURL
	if cartURL == "" {
		cartURL = baseURL + "/shoppingCart/?affiliate=28V"
	}

	sid := registerCartSession(client, cartURL, email)

	if discordBotTokenConfigured() {
		if err := sendCartDiscordWithButton(info, sid); err != nil {
			fmt.Printf("[discord] warning: Primary button message failed: %v\n", err)
		} else {
			fmt.Println("[discord] cart notification sent with Open Cart (Primary) button")
			return true
		}
	}

	openURL, err := openCartButtonURL(sid)
	if err != nil {
		unregisterCartSession(sid)
		fmt.Printf("[discord] warning: cart opener failed: %v\n", err)
		if err := NotifyCartReservedFromEnv(info); err != nil {
			fmt.Printf("[webhook] warning: %v\n", err)
		}
		return false
	}

	n, err := NewDiscordNotifierFromEnv()
	if err != nil {
		unregisterCartSession(sid)
		fmt.Printf("[webhook] warning: %v\n", err)
		return false
	}
	if err := n.NotifyCartReservedWithOpenURL(info, openURL); err != nil {
		unregisterCartSession(sid)
		fmt.Printf("[webhook] warning: %v\n", err)
		return false
	}
	fmt.Println("[discord] cart notification sent with Open Cart button — leave monitor running, then click it")
	return true
}

func waitForDiscordBot() {
	discordSessMu.Lock()
	s := discordSess
	discordSessMu.Unlock()
	fmt.Println("[discord] staying online — Open Cart clicks open Chrome; bot does NOT exit (Ctrl+C to quit)")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\n[discord] shutting down")
	if s != nil {
		_ = s.Close()
	}
	closeLiveBrowsers()
}

func waitUntilInterrupt(reason string) {
	fmt.Printf("[main] staying alive (%s) — Ctrl+C to quit\n", reason)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	fmt.Println("\n[main] shutting down")
	closeLiveBrowsers()
}

func closeRegisteredCartClients() {
	cartSessionsMu.Lock()
	defer cartSessionsMu.Unlock()
	seen := map[*Client]struct{}{}
	for _, sess := range cartSessions {
		if sess == nil || sess.Client == nil {
			continue
		}
		if _, ok := seen[sess.Client]; ok {
			continue
		}
		seen[sess.Client] = struct{}{}
		sess.Client.Close()
	}
}
