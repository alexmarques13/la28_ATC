package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	TicketPrice   string // per-ticket price
	TotalPrice    string // cart total
	Price         string // deprecated alias; prefer TicketPrice/TotalPrice
	SeatInfo      string // seat / section when available
	CartURL       string // shopping cart link for Open Cart button
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

// NotifyCartReservedWithOpenURL sends the cart embed plus an Open Cart button
// pointing at openURL (local cart opener). Requires with_components on the webhook POST.
func (n *DiscordNotifier) NotifyCartReservedWithOpenURL(info CartInfo, openURL string) error {
	if n == nil || n.WebhookURL == "" {
		return fmt.Errorf("discord webhook URL is not configured")
	}
	payload, err := buildDiscordCartPayloadWithButton(info, openURL)
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
	Content    string             `json:"content,omitempty"`
	Embeds     []discordEmbed     `json:"embeds"`
	Components []discordActionRow `json:"components,omitempty"`
}

type discordActionRow struct {
	Type       int                `json:"type"` // 1 = ACTION_ROW
	Components []discordComponent `json:"components"`
}

type discordComponent struct {
	Type     int    `json:"type"` // 2 = BUTTON
	Style    int    `json:"style,omitempty"`
	Label    string `json:"label,omitempty"`
	URL      string `json:"url,omitempty"`
	CustomID string `json:"custom_id,omitempty"`
}

func buildDiscordCartPayload(info CartInfo) ([]byte, error) {
	return buildDiscordCartPayloadWithButton(info, "")
}

func buildDiscordCartPayloadWithButton(info CartInfo, openCartURL string) ([]byte, error) {
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
	fields = append(fields,
		discordField("Venue", discordOrDash(info.Venue), true),
		discordField("Date / Time", discordOrDash(info.EventDateTime), false),
		discordField("Category", discordOrDash(info.Category), true),
		discordField("Quantity", discordFormatQty(info.Quantity), true),
		discordField("Ticket Price", discordOrDash(info.TicketPrice), true),
		discordField("Total", discordOrDash(info.TotalPrice), true),
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
	if u := strings.TrimSpace(openCartURL); u != "" {
		payload.Components = []discordActionRow{{
			Type: 1,
			Components: []discordComponent{{
				Type:  2,
				Style: 5, // Link — only component style Incoming Webhooks allow
				Label: "Open Cart",
				URL:   u,
			}},
		}}
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
	endpoint := n.WebhookURL
	// Required for message components (Open Cart button) on webhook messages.
	if strings.Contains(string(body), `"components"`) {
		if strings.Contains(endpoint, "?") {
			endpoint += "&with_components=true"
		} else {
			endpoint += "?with_components=true"
		}
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
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

func cartInfoFromBasket(email string, basket *BasketResult) CartInfo {
	qty := basket.Tickets
	if qty <= 0 {
		qty = basket.Quantity
	}
	evid := eventIDFromURL(basket.EventURL)
	info := CartInfo{
		EventName: eventLabelFromURL(basket.EventURL),
		Category:  formatCategoryDisplay(basket.Category),
		Quantity:  qty,
		CartURL:   basket.CartURL,
		Status:    discordDefaultStatus,
		Timestamp: time.Now().UTC(),
	}
	if info.CartURL == "" {
		info.CartURL = baseURL + "/shoppingCart/?affiliate=28V"
	}
	// Prefer the ATC'd unit price from the event page (authoritative).
	if basket.UnitPriceCents > 0 {
		info.TicketPrice = formatMoney(float64(basket.UnitPriceCents) / 100.0)
		info.TotalPrice = formatMoney(float64(basket.UnitPriceCents*qty) / 100.0)
	}
	enrichCartInfoFromJSON(&info, basket.Cart)
	enrichCartInfoFromEventMeta(&info, basket.EventURL, evid)
	finalizeCartPrices(&info, basket)
	if info.SeatInfo == "" {
		info.SeatInfo = findCartSeats(basket.Cart)
	}
	return info
}

func finalizeCartPrices(info *CartInfo, basket *BasketResult) {
	if info == nil {
		return
	}
	qty := info.Quantity
	if qty <= 0 {
		qty = 1
	}

	// Re-assert ATC unit price from the event page — ignore souvenir/upsell noise in cart JSON.
	if basket != nil && basket.UnitPriceCents > 0 {
		unit := float64(basket.UnitPriceCents) / 100.0
		info.TicketPrice = formatMoney(unit)
		info.TotalPrice = formatMoney(unit * float64(qty))
		return
	}

	info.TicketPrice = normalizeMoney(info.TicketPrice)
	info.TotalPrice = normalizeMoney(info.TotalPrice)

	if info.TotalPrice == "" {
		if total, ok := extractOrderTicketTotal(basket.Cart); ok {
			info.TotalPrice = formatMoney(total)
		}
	}
	if info.TicketPrice == "" {
		if u := extractOrderUnitPrice(basket.Cart); u > 0 {
			info.TicketPrice = formatMoney(u)
		}
	}
	if info.TicketPrice == "" && info.TotalPrice != "" {
		if total, ok := parseMoney(info.TotalPrice); ok {
			info.TicketPrice = formatMoney(total / float64(qty))
		}
	}
	if info.TotalPrice == "" && info.TicketPrice != "" {
		if unit, ok := parseMoney(info.TicketPrice); ok {
			info.TotalPrice = formatMoney(unit * float64(qty))
		}
	}
}

func extractOrderTicketTotal(raw json.RawMessage) (float64, bool) {
	order := cartOrderObject(raw)
	if order == nil {
		return 0, false
	}
	// Prefer explicit ticket/order totals — never memoryTicketConfigs / upsells.
	if v := firstCartString(order, "totalPriceFormatted", "formattedTotalPrice", "ticketsTotalFormatted", "ticketTotalFormatted"); v != "" {
		if n, ok := parseMoney(v); ok && n > 0 {
			return n, true
		}
	}
	for _, key := range []string{"totalPrice", "ticketsTotal", "ticketTotal", "totalPriceInt", "paymentTotal"} {
		switch n := order[key].(type) {
		case float64:
			if n <= 0 {
				continue
			}
			// Heuristic: large ints are cents.
			if n >= 1000 && n == float64(int64(n)) && !strings.Contains(strings.ToLower(key), "formatted") {
				if key == "totalPriceInt" || n > 1000 {
					return n / 100.0, true
				}
			}
			return n, true
		case json.Number:
			if f, err := n.Float64(); err == nil && f > 0 {
				if key == "totalPriceInt" {
					return f / 100.0, true
				}
				return f, true
			}
		}
	}
	// Sum ticket line prices inside order.tickets only.
	sum := 0.0
	count := 0
	if tickets, ok := order["tickets"].([]any); ok {
		for _, item := range tickets {
			t0, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if p := ticketLinePrice(t0); p > 0 {
				sum += p
				count++
			}
		}
	}
	if count > 0 {
		return sum, true
	}
	return 0, false
}

func extractOrderUnitPrice(raw json.RawMessage) float64 {
	order := cartOrderObject(raw)
	if order == nil {
		return 0
	}
	if tickets, ok := order["tickets"].([]any); ok {
		for _, item := range tickets {
			t0, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if p := ticketLinePrice(t0); p > 0 {
				return p
			}
		}
	}
	return 0
}

func ticketLinePrice(t0 map[string]any) float64 {
	if v := firstCartString(t0, "priceFormatted", "formattedPrice", "ticketPriceFormatted", "unitPriceFormatted", "priceText"); v != "" {
		if n, ok := parseMoney(v); ok {
			return n
		}
	}
	for _, key := range []string{"unitPrice", "ticketPrice", "price", "priceInt", "unitPriceInt"} {
		switch n := t0[key].(type) {
		case float64:
			if n <= 0 {
				continue
			}
			if strings.HasSuffix(strings.ToLower(key), "int") || n >= 1000 {
				return n / 100.0
			}
			return n
		case json.Number:
			if f, err := n.Float64(); err == nil && f > 0 {
				if strings.HasSuffix(strings.ToLower(key), "int") {
					return f / 100.0
				}
				return f
			}
		}
	}
	return 0
}

func cartOrderObject(raw json.RawMessage) map[string]any {
	var cart map[string]any
	if err := json.Unmarshal(raw, &cart); err != nil {
		return nil
	}
	if order, ok := cart["order"].(map[string]any); ok {
		return order
	}
	return nil
}

func normalizeMoney(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if n, ok := parseMoney(s); ok {
		return formatMoney(n)
	}
	return ensureMoneyPrefix(s)
}

func parseMoney(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	for strings.HasPrefix(s, "$") {
		s = strings.TrimPrefix(s, "$")
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, false
	}
	return v, true
}

func formatMoney(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func formatCategoryDisplay(cat string) string {
	cat = strings.TrimSpace(cat)
	if cat == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(cat), "category ") {
		return cat
	}
	if len(cat) == 1 {
		return "Category " + strings.ToUpper(cat)
	}
	return cat
}

// enrichCartInfoFromEventMeta fills CODE name / venue / datetime from filtered
// tickets or the live tracker (archery etc. may be absent from filtered_tickets.json).
func enrichCartInfoFromEventMeta(info *CartInfo, eventURL, evid string) {
	if info == nil {
		return
	}
	ft, ok := lookupEventMeta(eventURL, evid)
	if !ok {
		fmt.Printf("[webhook] event meta not found for evid=%s url=%s\n", evid, truncate(eventURL, 80))
		return
	}
	if label := formatEventCodeName(ft.Code, ft.Name); label != "" {
		info.EventName = label
	}
	if ft.Code != "" {
		info.EventID = ft.Code
	} else if evid != "" && info.EventID == "" {
		info.EventID = evid
	}
	if v := formatVenueDisplay(ft.Venue); v != "" {
		info.Venue = v
	}
	if dt := formatEventDateTimeDisplay(ft.DateTime); dt != "" {
		info.EventDateTime = dt
	}
	// Unit list price from tracker only when ATC page price was unavailable.
	if info.TicketPrice == "" && info.Category != "" {
		info.TicketPrice = normalizeMoney(priceForCategory(ft.Categories, info.Category))
	}
}

func priceForCategory(cats []trackerCategory, category string) string {
	want := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(category), "category ")))
	for _, c := range cats {
		letter := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(c.Name), "category ")))
		if letter == want || strings.EqualFold(c.Name, category) {
			if p := strings.TrimSpace(c.PriceRange); p != "" {
				return p
			}
			if c.Price > 0 {
				return fmt.Sprintf("$%.2f", c.Price)
			}
		}
	}
	return ""
}

var trackerMetaCache []trackerEvent

func lookupEventMeta(eventURL, eventID string) (filteredTicket, bool) {
	if ft, ok := lookupFilteredTicket(eventURL, eventID); ok {
		return ft, true
	}
	// Fall back to full tracker so events outside the price/conditions filter still resolve.
	if trackerMetaCache == nil {
		events, err := fetchTrackerEvents(trackerURL)
		if err != nil {
			fmt.Printf("[webhook] tracker lookup failed: %v\n", err)
			trackerMetaCache = []trackerEvent{} // avoid refetch storm
			return filteredTicket{}, false
		}
		for i := range events {
			events[i].Subtitle = cleanupText(events[i].Subtitle)
			events[i].DateTime = cleanupText(events[i].DateTime)
			for j := range events[i].Categories {
				events[i].Categories[j].PriceRange = cleanupPrice(events[i].Categories[j].PriceRange)
			}
		}
		trackerMetaCache = events
		fmt.Printf("[webhook] cached %d tracker events for meta lookup\n", len(events))
	}
	evid := strings.TrimSpace(eventID)
	if evid == "" {
		evid = eventIDFromURL(eventURL)
	}
	wantPath := eventPathKey(eventURL)
	for _, e := range trackerMetaCache {
		if evid != "" && eventIDFromURL(e.URL) == evid {
			return trackerEventToFiltered(e), true
		}
		if wantPath != "" && eventPathKey(e.URL) == wantPath {
			return trackerEventToFiltered(e), true
		}
	}
	return filteredTicket{}, false
}

func trackerEventToFiltered(e trackerEvent) filteredTicket {
	return filteredTicket{
		Code:       e.Code,
		Name:       e.Name,
		Subtitle:   e.Subtitle,
		DateTime:   e.DateTime,
		Venue:      e.Venue,
		Categories: e.Categories,
		URL:        e.URL,
	}
}

// enrichCartInfoFromFilteredTickets sets Event as "CODE Name" (e.g. "ARC07 Archery Mixed Preliminary")
// plus venue/datetime from filtered_tickets.json when the ATC event matches.
func enrichCartInfoFromFilteredTickets(info *CartInfo, eventURL string) {
	enrichCartInfoFromEventMeta(info, eventURL, "")
}

func formatEventCodeName(code, name string) string {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	switch {
	case code != "" && name != "":
		if strings.HasPrefix(strings.ToUpper(name), strings.ToUpper(code)+" ") {
			return name
		}
		return code + " " + name
	case name != "":
		return name
	default:
		return code
	}
}

func hasEventCodePrefix(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return false
	}
	code := parts[0]
	if len(code) < 3 {
		return false
	}
	letters, digits := 0, 0
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letters++
		case r >= '0' && r <= '9':
			digits++
		default:
			return false
		}
	}
	return letters > 0 && digits > 0
}

// formatEventDateTimeDisplay keeps tracker style:
// "Sunday, 07.23.2028 | 20:00‒22:35 Local Time (24-hour)"
func formatEventDateTimeDisplay(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Already in tracker form — only normalize the time-range dash.
	if strings.Contains(raw, "Local Time") && strings.Contains(raw, " | ") {
		return normalizeTimeRangeDash(raw)
	}

	// Try common parse layouts from cart / APIs, then render tracker form.
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"01/02/2006 15:04",
		"01.02.2006 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if start, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return start.Format("Monday, 01.02.2006 | 15:04") + " Local Time (24-hour)"
		}
	}
	return normalizeTimeRangeDash(raw)
}

func normalizeTimeRangeDash(s string) string {
	// 20:00-22:35 / 20:00–22:35 / 20:00—22:35 → 20:00‒22:35
	replacer := strings.NewReplacer(
		"–", "‒",
		"—", "‒",
	)
	s = replacer.Replace(s)
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '-' && i >= 5 && i+5 < len(runes) {
			// digit digit : digit digit - digit digit : digit digit
			if runes[i-3] == ':' && runes[i+3] == ':' &&
				isASCIIDigit(runes[i-5]) && isASCIIDigit(runes[i-4]) &&
				isASCIIDigit(runes[i-2]) && isASCIIDigit(runes[i-1]) &&
				isASCIIDigit(runes[i+1]) && isASCIIDigit(runes[i+2]) &&
				isASCIIDigit(runes[i+4]) && isASCIIDigit(runes[i+5]) {
				b.WriteRune('‒')
				continue
			}
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// formatVenueDisplay turns tracker venues like "Carson Stadium, Carson"
// into "CARSON, CA | Carson Stadium".
func formatVenueDisplay(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, " | ") {
		return raw
	}
	venue, city := splitVenueCity(raw)
	if city == "" {
		city = inferCityFromVenue(venue)
	}
	if city == "" {
		return raw
	}
	state := venueStateForCity(city)
	cityDisp := strings.ToUpper(city)
	if state != "" {
		return fmt.Sprintf("%s, %s | %s", cityDisp, state, venue)
	}
	return fmt.Sprintf("%s | %s", cityDisp, venue)
}

func splitVenueCity(raw string) (venue, city string) {
	// Common tracker form: "Carson Stadium, Carson" / "Intuit Dome, Inglewood"
	if i := strings.LastIndex(raw, ","); i > 0 {
		venue = strings.TrimSpace(raw[:i])
		city = strings.TrimSpace(raw[i+1:])
		return venue, city
	}
	return raw, ""
}

func inferCityFromVenue(venue string) string {
	low := strings.ToLower(venue)
	switch {
	case strings.Contains(low, "carson"):
		return "Carson"
	case strings.Contains(low, "long beach"):
		return "Long Beach"
	case strings.Contains(low, "inglewood"), strings.Contains(low, "intuit dome"), strings.Contains(low, "2028 stadium"):
		return "Inglewood"
	case strings.Contains(low, "dtla"), strings.Contains(low, "los angeles"), strings.Contains(low, "coliseum"), strings.Contains(low, "crypto"):
		return "Los Angeles"
	case strings.Contains(low, "pasadena"), strings.Contains(low, "rose bowl"):
		return "Pasadena"
	case strings.Contains(low, "pomona"), strings.Contains(low, "fairgrounds"):
		return "Pomona"
	case strings.Contains(low, "nashville"):
		return "Nashville"
	case strings.Contains(low, "columbus"):
		return "Columbus"
	case strings.Contains(low, "st. louis"), strings.Contains(low, "st louis"):
		return "St. Louis"
	case strings.Contains(low, "flushing"), strings.Contains(low, "new york"):
		return "New York"
	case strings.Contains(low, "san diego"):
		return "San Diego"
	case strings.Contains(low, "san jos"):
		return "San Jose"
	default:
		return ""
	}
}

func venueStateForCity(city string) string {
	switch strings.ToLower(strings.TrimSpace(city)) {
	case "carson", "long beach", "inglewood", "los angeles", "pasadena", "pomona",
		"san diego", "san jose", "san josé", "anaheim", "santa clarita":
		return "CA"
	case "nashville":
		return "TN"
	case "columbus":
		return "OH"
	case "st. louis", "st louis":
		return "MO"
	case "new york", "flushing":
		return "NY"
	default:
		// Default LA28 host region when city is unknown but venue is local-ish.
		return "CA"
	}
}

func lookupFilteredTicket(eventURL, eventID string) (filteredTicket, bool) {
	paths := []string{"filtered_tickets.json"}
	if v := strings.TrimSpace(os.Getenv("TICKETS_OUT")); v != "" {
		paths = append([]string{v}, paths...)
	}
	evid := strings.TrimSpace(eventID)
	if evid == "" {
		evid = eventIDFromURL(eventURL)
	}
	wantPath := eventPathKey(eventURL)

	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var list []filteredTicket
		if err := json.Unmarshal(b, &list); err != nil {
			continue
		}
		for _, ft := range list {
			if evid != "" && eventIDFromURL(ft.URL) == evid {
				return ft, true
			}
			if wantPath != "" && eventPathKey(ft.URL) == wantPath {
				return ft, true
			}
		}
	}
	return filteredTicket{}, false
}

func eventPathKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Path), "/")
}

func enrichCartInfoFromJSON(info *CartInfo, raw json.RawMessage) {
	if info == nil || len(raw) == 0 {
		return
	}
	var cart map[string]any
	if err := json.Unmarshal(raw, &cart); err != nil {
		return
	}
	order, _ := cart["order"].(map[string]any)
	if order == nil {
		order = cart
	}
	if info.TotalPrice == "" {
		if v := firstCartString(order,
			"totalPriceFormatted", "formattedTotalPrice", "totalPriceText"); v != "" {
			info.TotalPrice = ensureMoneyPrefix(v)
		} else if v := stringifyCartVal(order["totalPrice"]); v != "" {
			info.TotalPrice = ensureMoneyPrefix(v)
		}
	}
	if info.TicketPrice == "" {
		if v := firstCartString(order, "unitPriceFormatted", "formattedUnitPrice", "pricePerTicket"); v != "" {
			info.TicketPrice = ensureMoneyPrefix(v)
		}
	}
	if tickets, ok := order["tickets"].([]any); ok {
		seats := make([]string, 0, len(tickets))
		for _, item := range tickets {
			t0, ok := item.(map[string]any)
			if !ok {
				continue
			}
			title := firstCartString(t0, "eventTitle", "title", "eventName", "name")
			if code := firstCartString(t0, "eventCode", "code", "ticketEventCode"); code != "" {
				info.EventName = formatEventCodeName(code, title)
			} else if title != "" && !strings.Contains(info.EventName, " ") {
				info.EventName = title
			}
			if venue := firstCartString(t0, "venueName", "venue", "location"); venue != "" {
				info.Venue = formatVenueDisplay(venue)
			}
			if dt := firstCartString(t0, "eventDateTime", "dateTime", "startDate", "eventDate"); dt != "" {
				info.EventDateTime = formatEventDateTimeDisplay(dt)
			}
			if seat := buildSeatLabel(t0); seat != "" {
				seats = append(seats, seat)
			}
			if info.TicketPrice == "" {
				if p := firstCartString(t0, "priceFormatted", "formattedPrice", "priceText", "ticketPriceFormatted", "unitPriceFormatted"); p != "" {
					info.TicketPrice = ensureMoneyPrefix(p)
				} else if p := stringifyCartVal(t0["price"]); p != "" {
					info.TicketPrice = ensureMoneyPrefix(p)
				} else if p := stringifyCartVal(t0["unitPrice"]); p != "" {
					info.TicketPrice = ensureMoneyPrefix(p)
				}
			}
		}
		if len(seats) > 0 {
			info.SeatInfo = strings.Join(seats, ", ")
		}
	}
	if events, ok := order["events"].([]any); ok {
		for _, item := range events {
			ev, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if title := firstCartString(ev, "title", "name", "eventTitle"); title != "" {
				if !hasEventCodePrefix(info.EventName) {
					info.EventName = title
				}
			}
			if venue := firstCartString(ev, "venueName", "venue"); venue != "" {
				info.Venue = formatVenueDisplay(venue)
			}
			if dt := firstCartString(ev, "dateTime", "startDate", "eventDateTime"); dt != "" {
				info.EventDateTime = formatEventDateTimeDisplay(dt)
			}
			break
		}
	}
}

func buildSeatLabel(t0 map[string]any) string {
	parts := make([]string, 0, 4)
	if s := firstCartString(t0, "section", "block", "tribune", "stand"); s != "" {
		parts = append(parts, "Sec "+s)
	}
	if s := firstCartString(t0, "row"); s != "" {
		parts = append(parts, "Row "+s)
	}
	if s := firstCartString(t0, "seat", "seatNumber", "seatNo"); s != "" {
		parts = append(parts, "Seat "+s)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return firstCartString(t0, "seatInfo", "seatLabel", "placement", "areaName", "priceCategoryName")
}

func findCartTotalPrice(raw json.RawMessage) string {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	keys := []string{"totalPriceFormatted", "formattedTotalPrice", "totalPriceText"}
	if v := walkCartString(root, keys...); v != "" {
		return ensureMoneyPrefix(v)
	}
	if v := walkCartNumber(root, "totalPrice"); v > 0 {
		return formatMoney(v)
	}
	return ""
}

func findCartUnitPrice(raw json.RawMessage) string {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	keys := []string{
		"unitPriceFormatted", "formattedUnitPrice", "pricePerTicket",
		"ticketPriceFormatted", "priceFormatted", "formattedPrice",
	}
	if v := walkCartString(root, keys...); v != "" {
		return ensureMoneyPrefix(v)
	}
	if v := walkCartNumber(root, "unitPrice", "ticketPrice", "price"); v > 0 {
		return formatMoney(v)
	}
	return ""
}

func ensureMoneyPrefix(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if n, ok := parseMoney(v); ok {
		return formatMoney(n)
	}
	for strings.HasPrefix(v, "$$") {
		v = "$" + strings.TrimPrefix(v, "$$")
	}
	if strings.HasPrefix(v, "$") {
		return v
	}
	if isAllDigitsOrDot(strings.ReplaceAll(v, ",", "")) {
		return "$" + v
	}
	return v
}

func findCartPrice(raw json.RawMessage) string {
	if t := findCartTotalPrice(raw); t != "" {
		return t
	}
	return findCartUnitPrice(raw)
}

func findCartSeats(raw json.RawMessage) string {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	seats := walkCartSeats(root)
	if len(seats) == 0 {
		return ""
	}
	return strings.Join(seats, ", ")
}

func walkCartSeats(v any) []string {
	switch t := v.(type) {
	case map[string]any:
		if seat := buildSeatLabel(t); seat != "" {
			return []string{seat}
		}
		out := []string{}
		for _, child := range t {
			out = append(out, walkCartSeats(child)...)
		}
		return out
	case []any:
		out := []string{}
		for _, child := range t {
			out = append(out, walkCartSeats(child)...)
		}
		return out
	default:
		return nil
	}
}

func walkCartString(v any, keys ...string) string {
	switch t := v.(type) {
	case map[string]any:
		if s := firstCartString(t, keys...); s != "" {
			return s
		}
		for _, child := range t {
			if s := walkCartString(child, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range t {
			if s := walkCartString(child, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

func walkCartNumber(v any, keys ...string) float64 {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range keys {
			switch n := t[k].(type) {
			case float64:
				if n > 0 {
					return n
				}
			case json.Number:
				if f, err := n.Float64(); err == nil && f > 0 {
					return f
				}
			}
		}
		for _, child := range t {
			if n := walkCartNumber(child, keys...); n > 0 {
				return n
			}
		}
	case []any:
		for _, child := range t {
			if n := walkCartNumber(child, keys...); n > 0 {
				return n
			}
		}
	}
	return 0
}

func isAllDigitsOrDot(s string) bool {
	if s == "" {
		return false
	}
	dot := 0
	for _, r := range s {
		if r == '.' {
			dot++
			if dot > 1 {
				return false
			}
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func firstCartString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := stringifyCartVal(m[k]); v != "" {
			return v
		}
	}
	return ""
}

func eventLabelFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "(unknown event)"
	}
	u := strings.TrimSuffix(raw, "/")
	if i := strings.Index(u, "?"); i >= 0 {
		u = u[:i]
	}
	parts := strings.Split(u, "/")
	slug := parts[len(parts)-1]
	if slug == "" && len(parts) > 1 {
		slug = parts[len(parts)-2]
	}
	slug = strings.ReplaceAll(slug, "-", " ")
	if slug == "" {
		return raw
	}
	return slug
}

func stringifyCartVal(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%.0f", t)
		}
		return fmt.Sprintf("%v", t)
	case json.Number:
		return t.String()
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
