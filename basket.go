package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type eventTicketType struct {
	ID        string
	Name      string
	Field     string // rs_menge_<id>
	PriceID   string // data-cc-price-id
	UnitCents int
	MaxAmount int // from input max / data-max when present (0 = unknown)
}

const defaultMaxEvenQty = 8 // used when the event page does not expose a max

// evenFloor returns the largest even integer <= n, or 0 if that would be < 2.
func evenFloor(n int) int {
	if n < 2 {
		return 0
	}
	return n - (n % 2)
}

func requireEvenQty(qty int) (int, error) {
	if qty%2 != 0 {
		return 0, fmt.Errorf("quantity must be even (got %d) — odd ATC is not allowed", qty)
	}
	if qty < 2 {
		return 0, fmt.Errorf("quantity must be at least 2 (got %d)", qty)
	}
	return qty, nil
}

type eventPriceCategory struct {
	Letter      string // A, B, C...
	Name        string // Category C
	PKID        string
	Action      string // form action (add-to-basket endpoint)
	Hidden      map[string]string
	HiddenOrder []string // DOM order of hidden input names
	TicketTypes []eventTicketType
}

type BasketResult struct {
	EventURL       string          `json:"eventUrl"`
	Category       string          `json:"category"`
	Quantity       int             `json:"quantity"`
	PKID           string          `json:"pkId"`
	TicketType     string          `json:"ticketTypeId"`
	Tickets        int             `json:"ticketsInCart"`
	UnitPriceCents int             `json:"unitPriceCents,omitempty"`
	Cart           json.RawMessage `json:"cart"`
	CartURL        string          `json:"cartUrl,omitempty"`
}

var (
	reFormBlock   = regexp.MustCompile(`(?is)<form\b([^>]*)name="pk(\d+)"([^>]*)>(.*?)</form>`)
	reCatQA       = regexp.MustCompile(`data-qa="pricecategoryCategory\s*([A-Z])"`)
	reTTConfig    = regexp.MustCompile(`(?s)<script type="application/configuration">\s*(\{.*? "ticketTypeId"\s*:\s*"(\d+)".*?\})\s*</script>`)
	reAmountInput = regexp.MustCompile(`(?is)<input[^>]*id="amount_input_(\d+)"[^>]*>`)
	reHiddenInput = regexp.MustCompile(`(?is)<input[^>]*type=["']hidden["'][^>]*>`)
	reAttr        = regexp.MustCompile(`([a-zA-Z0-9:-]+)="([^"]*)"`)
	reInitialPath = regexp.MustCompile(`"initialRequestPath"\s*:\s*"([^"]+)"`)
)

// AddFilteredToBasket ATCs only events/categories from the filter output.
// For each target it requests the largest even quantity allowed (never odd),
// stepping down by 2 until the cart accepts or the target is exhausted.
func (c *Client) AddFilteredToBasket(tickets []filteredTicket, maxWant int) (*BasketResult, error) {
	targets := atcTargetsFromFiltered(tickets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no ATC targets in filtered tickets — refuse freeform event ATC")
	}
	maxWant = evenFloor(maxWant)
	if maxWant < 2 {
		maxWant = defaultMaxEvenQty
	}
	fmt.Printf("[basket] ATC from %d filtered target(s), max even qty=%d\n", len(targets), maxWant)

	var lastErr error
	for i, t := range targets {
		fmt.Printf("[basket] filtered target %d/%d: %s category %s\n", i+1, len(targets), t.Label, t.Category)
		basket, err := c.AddToBasketMaxEven(t.URL, t.Category, maxWant)
		if err == nil {
			return basket, nil
		}
		lastErr = err
		fmt.Printf("[basket] target failed: %v\n", err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no filtered ATC target succeeded")
	}
	return nil, lastErr
}

type atcTarget struct {
	URL      string
	Category string
	Label    string
}

func atcTargetsFromFiltered(tickets []filteredTicket) []atcTarget {
	var out []atcTarget
	for _, ft := range tickets {
		url := strings.TrimSpace(ft.URL)
		if url == "" {
			continue
		}
		for _, letter := range categoryLettersFromFiltered(ft) {
			label := strings.TrimSpace(ft.Code + " " + ft.Name)
			if label == "" {
				label = url
			}
			out = append(out, atcTarget{URL: url, Category: letter, Label: label})
		}
	}
	return out
}

func categoryLettersFromFiltered(ft filteredTicket) []string {
	seen := map[string]bool{}
	var letters []string
	for _, c := range ft.Categories {
		letter, ok := categoryLetter(c.Name)
		if !ok {
			continue
		}
		s := string(letter)
		if seen[s] {
			continue
		}
		seen[s] = true
		letters = append(letters, s)
	}
	// Fallback: parse catFilter like "A" or "A to C"
	if len(letters) == 0 && strings.TrimSpace(ft.CatFilter) != "" {
		raw := strings.ToUpper(strings.TrimSpace(ft.CatFilter))
		if m := regexp.MustCompile(`([A-Z])\s+TO\s+([A-Z])`).FindStringSubmatch(raw); len(m) == 3 {
			lo, hi := rune(m[1][0]), rune(m[2][0])
			if lo > hi {
				lo, hi = hi, lo
			}
			for r := lo; r <= hi; r++ {
				letters = append(letters, string(r))
			}
		} else {
			for _, r := range raw {
				if r >= 'A' && r <= 'Z' {
					s := string(r)
					if !seen[s] {
						seen[s] = true
						letters = append(letters, s)
					}
				}
			}
		}
	}
	return letters
}

// AddToBasketMaxEven loads the event once and tries the largest even qty
// (capped by page max / maxWant), then  max-2, max-4, … down to 2.
func (c *Client) AddToBasketMaxEven(eventURL, categoryLetter string, maxWant int) (*BasketResult, error) {
	maxWant = evenFloor(maxWant)
	if maxWant < 2 {
		maxWant = defaultMaxEvenQty
	}

	categoryLetter = strings.ToUpper(strings.TrimSpace(categoryLetter))
	if categoryLetter == "" {
		return nil, fmt.Errorf("category letter required")
	}

	pageMax, err := c.peekEventCategoryMax(eventURL, categoryLetter)
	if err != nil {
		return nil, err
	}
	start := maxWant
	if pageMax > 0 {
		start = evenFloor(pageMax)
		if start > maxWant {
			start = maxWant
		}
	}
	if start < 2 {
		return nil, fmt.Errorf("no even quantity available for category %s (pageMax=%d maxWant=%d)", categoryLetter, pageMax, maxWant)
	}
	fmt.Printf("[basket] trying even quantities from %d down to 2 (pageMax=%d)\n", start, pageMax)

	var lastErr error
	for qty := start; qty >= 2; qty -= 2 {
		basket, err := c.AddToBasket(eventURL, categoryLetter, qty)
		if err == nil {
			return basket, nil
		}
		lastErr = err
		fmt.Printf("[basket] qty %d failed: %v\n", qty, err)
		// Hard auth / akamai failures — don't burn lower qtys on the same session issue.
		msg := err.Error()
		if strings.Contains(msg, "refuse ATC") || strings.Contains(msg, "Access Denied") ||
			strings.Contains(msg, "login=") {
			return nil, err
		}
	}
	return nil, lastErr
}

// peekEventCategoryMax loads the event page and returns the Standard ticket max amount (0 if unknown).
func (c *Client) peekEventCategoryMax(eventURL, categoryLetter string) (int, error) {
	page, err := c.getProtected(eventURL, baseURL+"/")
	if err != nil {
		return 0, err
	}
	cats, err := parseEventCategories(page.Text)
	if err != nil {
		return 0, err
	}
	cat, ok := cats[strings.ToUpper(categoryLetter)]
	if !ok {
		return 0, fmt.Errorf("category %s not found on event", categoryLetter)
	}
	tt := pickStandardTicketType(cat)
	if tt == nil {
		return 0, fmt.Errorf("no ticket type for category %s", categoryLetter)
	}
	return tt.MaxAmount, nil
}

// AddToBasket opens an event page, selects the given price category letter
// (e.g. "C"), and adds tickets via CapacityControl's PUT /api/shoppingCart
// (addBestSeatJson). Quantity MUST be even (>= 2). That is cart hold only — not payment.
func (c *Client) AddToBasket(eventURL, categoryLetter string, qty int) (*BasketResult, error) {
	categoryLetter = strings.ToUpper(strings.TrimSpace(categoryLetter))
	if categoryLetter == "" {
		return nil, fmt.Errorf("category letter required")
	}
	var err error
	qty, err = requireEvenQty(qty)
	if err != nil {
		return nil, err
	}

	eventAffiliate := "28V"
	if u, err := url.Parse(eventURL); err == nil {
		if a := u.Query().Get("affiliate"); a != "" {
			eventAffiliate = a
		}
	}
	affiliate := eventAffiliate
	// Prefer a real logged-in webid on the SAME affiliate as the event (28V).
	// 28A cannot sell this event (marketing shell); missing 28V session → -3027.
	if c.webshopField(eventAffiliate, "login") != "1" || strings.Contains(c.webidValue(eventAffiliate), "NO_SESSION") {
		return nil, fmt.Errorf("%s.webshop login=%s webid=%s — refuse ATC (CapacityControl -3027 without real session)",
			eventAffiliate, c.webshopField(eventAffiliate, "login"), truncate(c.webidValue(eventAffiliate), 40))
	}

	fmt.Printf("[basket] loading event %s (category %s x%d)\n", eventURL, categoryLetter, qty)
	page, err := c.getProtected(eventURL, baseURL+"/")
	if err != nil {
		return nil, err
	}

	// Browser success: GET /api/shoppingCart yields 32-hex xsrfToken; same value
	// is webshop token= and the PUT ?token=.
	xsrf, err := c.fetchCartXSRF(page.URL)
	if err != nil {
		fmt.Printf("[basket] cart warm skipped: %v\n", err)
		if wx, e2 := c.extractXSRF(page.Text); e2 == nil {
			xsrf = wx
		}
	}

	cats, err := parseEventCategories(page.Text)
	if err != nil {
		return nil, err
	}
	cat, ok := cats[categoryLetter]
	if !ok {
		letters := make([]string, 0, len(cats))
		for k := range cats {
			letters = append(letters, k)
		}
		return nil, fmt.Errorf("category %s not found on event (have %v)", categoryLetter, letters)
	}
	tt := pickStandardTicketType(cat)
	if tt == nil {
		return nil, fmt.Errorf("no ticket type found for category %s", categoryLetter)
	}
	fmt.Printf("[basket] pk_id=%s ticketType=%s (%s) field=%s priceId=%s\n",
		cat.PKID, tt.ID, tt.Name, tt.Field, tt.PriceID)
	c.logAkamaiCookies("pre-atc")

	backPath := eventURL
	if u, err := url.Parse(eventURL); err == nil {
		backPath = u.RequestURI()
	}
	evID := eventIDFromURL(eventURL)
	if evID == "" {
		evID = cat.Hidden["ev_id"]
	}

	// Prefer cart xsrf (32-hex). R() only if no real cart token.
	atcToken := xsrf
	if atcToken == "" || strings.Contains(atcToken, "PLACEHOLDER") {
		atcToken = c.ensureWebshopATCToken(affiliate, page.Text, xsrf)
	} else {
		_ = c.ensureWebshopATCToken(affiliate, page.Text, atcToken)
		fmt.Printf("[basket] ATC url token (cart xsrf)=%s\n", truncate(atcToken, 20))
	}

	buildPutURL := func(token string) string {
		return fmt.Sprintf("%s/api/shoppingCart/?affiliate=%s&token=%s&evid=%s",
			baseURL, url.QueryEscape(affiliate), url.QueryEscape(token), url.QueryEscape(evID))
	}

	putURL := buildPutURL(atcToken)
	fmt.Printf("[basket] add-to-cart PUT %s\n", truncate(putURL, 120))

	promoID := cat.Hidden["promo_id"]
	if promoID == "" {
		promoID = "0"
	}
	priceID, _ := strconv.Atoi(tt.PriceID)
	// Live Chrome Category C success: STRING ids/amounts + ticketPriceIds +
	// ttIdToCustomVoucherValue (empty). Numeric/lean body was a wrong guess.
	type atcSeat struct {
		TicketTypeID             string `json:"ticketTypeId"`
		CombinedTicketTypeRuleID string `json:"combinedTicketTypeRuleId"`
		Amount                   string `json:"amount"`
	}
	type atcPayload struct {
		EventID                  string         `json:"eventId"`
		Timestamp                int64          `json:"timestamp"`
		TicketPriceIds           map[string]int `json:"ticketPriceIds"`
		TtIdToCustomVoucherValue map[string]any `json:"ttIdToCustomVoucherValue"`
		AddBestSeatJson          struct {
			RequestedBestSeats []atcSeat `json:"requestedBestSeats"`
		} `json:"addBestSeatJson"`
		BackToPrevPage string `json:"backToPrevPage"`
		PkID           string `json:"pkId"`
		PromotionID    string `json:"promotionId"`
	}
	payload := atcPayload{
		EventID:   evID,
		Timestamp: time.Now().UnixMilli(),
		TicketPriceIds: map[string]int{
			tt.ID: priceID,
		},
		TtIdToCustomVoucherValue: map[string]any{},
		BackToPrevPage:           backPath,
		PkID:                     cat.PKID,
		PromotionID:              promoID,
	}
	payload.AddBestSeatJson.RequestedBestSeats = []atcSeat{{
		TicketTypeID:             tt.ID,
		CombinedTicketTypeRuleID: "0",
		Amount:                   strconv.Itoa(qty),
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[basket] json body: %s\n", truncate(string(body), 320))

	// CRITICAL: do NOT send x-xsrf-token on this PUT. CapacityControl only puts
	// the token in the query string; the header triggers Akamai #18 Access Denied.
	headers := c.atcPutHeaders(page.URL)

	// Re-bind to the event-page sensor script and always complete 3 posts.
	// Skipping after cart-warm causes intermittent Akamai #18 on PUT.
	fmt.Println("[basket] event-page Akamai sensors before ATC")
	if err := c.solveAkamaiForURLOpts(page.URL, page.Text, true, true); err != nil {
		fmt.Printf("[basket] sensor refresh warning: %v\n", err)
	}
	c.logAkamaiCookies("pre-put")
	time.Sleep(500 * time.Millisecond)

	var resp *httpResult
	ccToken := ""
	var lastAppErr error
	for attempt := 0; attempt < 8; attempt++ {
		payload.Timestamp = time.Now().UnixMilli()
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		putURL = buildPutURL(atcToken)
		if ccToken != "" {
			putURL += "&ccToken=" + url.QueryEscape(ccToken)
		}

		resp, err = c.put(putURL, headers, strings.NewReader(string(body)), false)
		if err != nil {
			if lastAppErr != nil {
				return nil, lastAppErr
			}
			return nil, err
		}
		fmt.Printf("[basket] PUT %d → %s (%d bytes)\n", resp.StatusCode, truncate(resp.URL, 100), len(resp.Body))
		if len(resp.Body) > 0 && len(resp.Body) < 700 {
			fmt.Printf("[basket] body: %s\n", truncate(strings.ReplaceAll(resp.Text, "\n", " "), 400))
		} else if len(resp.Body) >= 700 {
			fmt.Printf("[basket] body head: %s\n", truncate(strings.ReplaceAll(resp.Text, "\n", " "), 220))
		}

		if solved, sErr := c.solveSecCptIfPresent(page.URL, resp.Text); sErr != nil {
			fmt.Printf("[basket] sec-cpt warning: %v\n", sErr)
		} else if solved {
			_ = c.postSensors(page.URL, false, 1)
			continue
		}
		if isSbsdChallenge(resp.Text) {
			fmt.Println("[basket] SBSD on ATC response — solving")
			if _, err := c.solveSbsdIfPresent(page.URL, resp.Text); err != nil {
				fmt.Printf("[basket] sbsd warning: %v\n", err)
			}
			_ = c.postSensors(page.URL, false, maxSensorPosts)
			continue
		}
		if resp.StatusCode == 403 && strings.Contains(resp.Text, "Access Denied") {
			fmt.Printf("[basket] 403 Access Denied (attempt %d)\n", attempt+1)
			if attempt >= 2 {
				return nil, fmt.Errorf("add-to-cart blocked (403 Access Denied) after %d tries — Akamai #18", attempt+1)
			}
			_ = c.postSensorsOpts(page.URL, true, maxSensorPosts, true)
			time.Sleep(1200 * time.Millisecond)
			continue
		}
		if resp.StatusCode == 428 || (resp.StatusCode == 403 && strings.Contains(resp.Text, "sec-cp")) {
			fmt.Printf("[basket] challenge status %d — retrying\n", resp.StatusCode)
			_ = c.postSensors(page.URL, false, 1)
			continue
		}
		if resp.StatusCode >= 400 && (strings.Contains(resp.Text, "Access Denied") ||
			strings.Contains(resp.Text, "Bad Request") || strings.Contains(resp.Text, "edgesuite")) {
			fmt.Printf("[basket] HTTP %d edge/html (attempt %d)\n", resp.StatusCode, attempt+1)
			// #169: stop thrashing — return prior app error or this edge error.
			if lastAppErr != nil {
				return nil, lastAppErr
			}
			if attempt >= 1 {
				return nil, fmt.Errorf("add-to-cart edge %d: %s", resp.StatusCode, truncate(resp.Text, 180))
			}
			_ = c.postSensorsOpts(page.URL, false, maxSensorPosts, true)
			time.Sleep(1000 * time.Millisecond)
			continue
		}

		var atcObj map[string]any
		if err := json.Unmarshal(resp.Body, &atcObj); err != nil {
			break
		}
		if tok, ok := atcObj["ccToken"].(string); ok && tok != "" {
			ccToken = tok
		}
		if wt, ok := atcObj["waitTime"].(float64); ok {
			wait := time.Duration(wt) * time.Millisecond
			if wait < 1500*time.Millisecond {
				wait = 1500 * time.Millisecond
			}
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			fmt.Printf("[basket] ATC waitTime=%v — sleeping %s then retry\n", atcObj["waitTime"], wait)
			time.Sleep(wait)
			continue
		}
		if v, ok := atcObj["openAddToCartFlyout"].(bool); ok && v {
			fmt.Println("[basket] openAddToCartFlyout=true")
			break
		}
		if ec, ok := atcObj["errorCode"].(float64); ok && int(ec) != 0 {
			fmt.Printf("[basket] ATC errorCode=%v msg=%v\n", atcObj["errorCode"], atcObj["errorMessage"])
			lastAppErr = fmt.Errorf("add-to-cart errorCode=%v msg=%v", atcObj["errorCode"], atcObj["errorMessage"])
			if int(ec) == -2100 {
				return nil, lastAppErr
			}
			// -3027 includes a ccToken + error path. Browser navigates to the error
			// page; do NOT replay that ccToken (it triggers Akamai #169). Wait and
			// retry a clean PUT after a light sensor refresh.
			ccToken = ""
			wait := 5 * time.Second
			fmt.Printf("[basket] waiting %s before clean ATC retry\n", wait)
			time.Sleep(wait)
			_ = c.postSensors(page.URL, false, maxSensorPosts)
			continue
		}
		if path, ok := atcObj["path"].(string); ok && path != "" && atcObj["captchaKey"] == nil {
			if strings.Contains(path, "error") {
				fmt.Printf("[basket] ATC redirected to error path: %s\n", truncate(path, 120))
				time.Sleep(2 * time.Second)
				continue
			}
			fmt.Printf("[basket] ATC path=%s\n", truncate(path, 120))
			break
		}
		break
	}
	if resp == nil {
		if lastAppErr != nil {
			return nil, lastAppErr
		}
		return nil, fmt.Errorf("add-to-cart produced no response")
	}
	if resp.StatusCode == 403 && strings.Contains(resp.Text, "Access Denied") {
		return nil, fmt.Errorf("add-to-cart blocked (403 Access Denied) — Akamai rejected the request (check Hyper sensor/_abck stop-signal; egress IP=%s)", c.ip)
	}
	if resp.StatusCode >= 400 {
		if lastAppErr != nil {
			return nil, lastAppErr
		}
		return nil, fmt.Errorf("add-to-cart failed: %d %s", resp.StatusCode, truncate(resp.Text, 240))
	}

	var atcObj map[string]any
	if err := json.Unmarshal(resp.Body, &atcObj); err == nil {
		if ec, ok := atcObj["errorCode"].(float64); ok && int(ec) != 0 {
			return nil, fmt.Errorf("add-to-cart errorCode=%v msg=%v", atcObj["errorCode"], atcObj["errorMessage"])
		}
		if path, ok := atcObj["path"].(string); ok && strings.Contains(path, "error") {
			return nil, fmt.Errorf("add-to-cart error path: %s", truncate(path, 160))
		}
	}

	cartReferer := fmt.Sprintf("%s/shoppingCart/?affiliate=%s", baseURL, url.QueryEscape(affiliate))
	cartBody, tickets, err := c.pollShoppingCart(affiliate, xsrf, cartReferer, 20, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &BasketResult{
		EventURL:       eventURL,
		Category:       categoryLetter,
		Quantity:       qty,
		PKID:           cat.PKID,
		TicketType:     tt.ID,
		Tickets:        tickets,
		UnitPriceCents: tt.UnitCents,
		Cart:           cartBody,
		CartURL:        cartReferer,
	}, nil
}

func parseInitialRequestPath(html, affiliate, xsrf string) string {
	m := reInitialPath.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	path := strings.ReplaceAll(m[1], `\u0026`, "&")
	path = strings.ReplaceAll(path, "_XSRF_PLACEHOLDER_", url.QueryEscape(xsrf))
	if !strings.HasPrefix(path, "http") {
		path = baseURL + path
	}
	if affiliate != "" && !strings.Contains(path, "affiliate=") {
		sep := "&"
		if !strings.Contains(path, "?") {
			sep = "?"
		}
		path += sep + "affiliate=" + url.QueryEscape(affiliate)
	}
	return path
}

func isPaymentCheckoutURL(raw string) bool {
	low := strings.ToLower(raw)
	if strings.Contains(low, "shoppingcart") || strings.Contains(low, "doc=shoppingcart") {
		return false
	}
	// Eventim payment / address / payment-method steps (not the ATC form endpoint).
	return strings.Contains(low, "/payment") ||
		strings.Contains(low, "doc=payment") ||
		strings.Contains(low, "doc=customerdata") ||
		strings.Contains(low, "doc=shipping") ||
		strings.Contains(low, "ordercomplete")
}

func (c *Client) logAkamaiCookies(label string) {
	names := []string{"_abck", "bm_sz", "bm_sv", "ak_bmsc", "sec_cpt", "sbsd_o", "bm_so"}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		v := c.cookie(n)
		if v == "" {
			continue
		}
		tail := v
		if len(tail) > 28 {
			tail = "..." + tail[len(tail)-28:]
		}
		parts = append(parts, fmt.Sprintf("%s=%s", n, tail))
	}
	fmt.Printf("[cookies:%s] %s\n", label, strings.Join(parts, " | "))
}

func (c *Client) warmShoppingCartAPI(affiliate, xsrf, referer string) {
	cartURL := fmt.Sprintf("%s/api/shoppingCart/?affiliate=%s&token=%s",
		baseURL, url.QueryEscape(affiliate), url.QueryEscape(xsrf))
	r, err := c.get(cartURL, c.apiHeaders(referer, map[string]string{
		"x-xsrf-token": xsrf,
		"accept":       "application/json, text/plain, */*",
	}), true)
	if err != nil {
		fmt.Printf("[basket] cart warm err: %v\n", err)
		return
	}
	fmt.Printf("[basket] cart warm → %d (%d bytes)\n", r.StatusCode, len(r.Body))
	if solved, sErr := c.solveSecCptIfPresent(referer, r.Text); sErr != nil {
		fmt.Printf("[basket] cart warm sec-cpt: %v\n", sErr)
	} else if solved {
		fmt.Println("[basket] cart warm solved sec-cpt")
	}
	if isSbsdChallenge(r.Text) {
		if _, err := c.solveSbsdIfPresent(referer, r.Text); err != nil {
			fmt.Printf("[basket] cart warm sbsd: %v\n", err)
		}
	}
}

// buildATCFormBody encodes the Eventim best-seat form in DOM-ish order.
// url.Values.Encode sorts alphabetically — Akamai/WAF can fingerprint that.
func buildATCFormBody(cat eventPriceCategory, tt *eventTicketType, xsrf, affiliate, eventURL, backPath string, qty int) string {
	type kv struct{ k, v string }
	var pairs []kv
	add := func(k, v string) {
		pairs = append(pairs, kv{k, v})
	}

	// Canonical Eventim order first.
	add("token", xsrf)
	add("fun", "wk")
	add("doc", "shoppingCart")
	add("affiliate", affiliate)
	add("ev_id", eventIDFromURL(eventURL))
	add("pk_id", cat.PKID)
	add("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	add("backToPrevPage", backPath)
	add("errordoc", "globalError")

	seen := map[string]bool{
		"token": true, "fun": true, "doc": true, "affiliate": true,
		"ev_id": true, "pk_id": true, "timestamp": true,
		"backToPrevPage": true, "errordoc": true,
	}
	promoSet := false
	for _, name := range cat.HiddenOrder {
		if seen[name] {
			continue
		}
		v := cat.Hidden[name]
		if name == "token" && (v == "" || strings.Contains(v, "PLACEHOLDER")) {
			continue
		}
		if strings.HasPrefix(name, "rs_menge_") {
			continue
		}
		add(name, v)
		seen[name] = true
		if name == "promo_id" {
			promoSet = true
		}
	}
	for k, v := range cat.Hidden {
		if seen[k] || strings.HasPrefix(k, "rs_menge_") {
			continue
		}
		if k == "token" && (v == "" || strings.Contains(v, "PLACEHOLDER")) {
			continue
		}
		add(k, v)
		seen[k] = true
		if k == "promo_id" {
			promoSet = true
		}
	}
	if !promoSet {
		add("promo_id", "0")
	}
	for _, other := range cat.TicketTypes {
		if other.Field == tt.Field {
			add(other.Field, strconv.Itoa(qty))
		} else {
			add(other.Field, "0")
		}
	}

	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.v))
	}
	return b.String()
}

func (c *Client) fetchCartXSRF(referer string) (string, error) {
	r, err := c.get(baseURL+"/api/shoppingCart/?affiliate=28V", c.apiGetHeaders(referer, nil), true)
	if err != nil {
		return "", err
	}
	fmt.Printf("[basket] cart warm → %d (%d bytes)\n", r.StatusCode, len(r.Body))
	var obj map[string]any
	if err := json.Unmarshal(r.Body, &obj); err != nil {
		return "", fmt.Errorf("cart not json: %s", truncate(r.Text, 200))
	}
	t, _ := obj["xsrfToken"].(string)
	if t == "" {
		return "", fmt.Errorf("no xsrfToken in cart response")
	}
	return t, nil
}

func (c *Client) pollShoppingCart(affiliate, xsrf, referer string, maxPolls int, interval time.Duration) (json.RawMessage, int, error) {
	for i := 0; i < maxPolls; i++ {
		cartURL := fmt.Sprintf("%s/api/shoppingCart/?affiliate=%s&token=%s",
			baseURL, url.QueryEscape(affiliate), url.QueryEscape(xsrf))
		r, err := c.get(cartURL, c.apiGetHeaders(referer, map[string]string{
			"x-xsrf-token": xsrf,
			"accept":       "application/json, text/plain, */*",
		}), true)
		if err != nil {
			return nil, 0, err
		}
		var obj map[string]any
		if err := json.Unmarshal(r.Body, &obj); err != nil {
			fmt.Printf("[basket] cart poll %d non-json %d %s\n", i+1, r.StatusCode, truncate(r.Text, 160))
			time.Sleep(interval)
			continue
		}
		if t, ok := obj["xsrfToken"].(string); ok && t != "" {
			xsrf = t
		}
		n := 0
		if order, ok := obj["order"].(map[string]any); ok {
			if v, ok := order["numberOfTickets"].(float64); ok {
				n = int(v)
			}
		}
		fmt.Printf("[basket] cart poll %d: tickets=%d page=%v\n", i+1, n, obj["page"])
		if n > 0 {
			raw, _ := json.Marshal(obj)
			return raw, n, nil
		}
		time.Sleep(interval)
	}
	return nil, 0, fmt.Errorf("shopping cart still empty after %d polls", maxPolls)
}

func parseEventCategories(html string) (map[string]eventPriceCategory, error) {
	out := map[string]eventPriceCategory{}
	forms := reFormBlock.FindAllStringSubmatch(html, -1)
	if len(forms) == 0 {
		return nil, fmt.Errorf("no price-category forms found on event page")
	}
	for _, fm := range forms {
		pk := fm[2]
		formAttrs := fm[1] + " " + fm[3]
		block := fm[4]
		letter := ""
		if m := reCatQA.FindStringSubmatch(block); len(m) == 2 {
			letter = m[1]
		}
		if letter == "" {
			// sometimes data-qa is on a parent wrapper just outside inputs — search attrs+body
			if m := reCatQA.FindStringSubmatch(fm[0]); len(m) == 2 {
				letter = m[1]
			}
		}
		if letter == "" {
			continue
		}
		action := ""
		attrs := map[string]string{}
		for _, am := range reAttr.FindAllStringSubmatch(formAttrs, -1) {
			attrs[am[1]] = am[2]
		}
		action = attrs["action"]
		hidden := map[string]string{}
		hiddenOrder := []string{}
		for _, tag := range reHiddenInput.FindAllString(block, -1) {
			ha := map[string]string{}
			for _, am := range reAttr.FindAllStringSubmatch(tag, -1) {
				ha[am[1]] = am[2]
			}
			if name := ha["name"]; name != "" {
				if _, exists := hidden[name]; !exists {
					hiddenOrder = append(hiddenOrder, name)
				}
				hidden[name] = ha["value"]
			}
		}
		cat := eventPriceCategory{
			Letter:      letter,
			Name:        "Category " + letter,
			PKID:        pk,
			Action:      action,
			Hidden:      hidden,
			HiddenOrder: hiddenOrder,
		}
		for _, im := range reAmountInput.FindAllStringSubmatch(block, -1) {
			tag := im[0]
			id := im[1]
			ia := map[string]string{}
			for _, am := range reAttr.FindAllStringSubmatch(tag, -1) {
				ia[am[1]] = am[2]
			}
			tt := eventTicketType{
				ID:      id,
				Field:   "rs_menge_" + id,
				Name:    ia["data-discount-level-name"],
				PriceID: ia["data-cc-price-id"],
			}
			if v := ia["data-unit-value"]; v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					tt.UnitCents = n
				}
			}
			tt.MaxAmount = parseAmountMax(ia)
			if tt.Name == "" {
				tt.Name = "ticket"
			}
			cat.TicketTypes = append(cat.TicketTypes, tt)
		}
		out[letter] = cat
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parsed forms but no categories")
	}
	return out, nil
}

func pickStandardTicketType(cat eventPriceCategory) *eventTicketType {
	for i := range cat.TicketTypes {
		if strings.EqualFold(cat.TicketTypes[i].Name, "Standard") {
			return &cat.TicketTypes[i]
		}
	}
	if len(cat.TicketTypes) > 0 {
		return &cat.TicketTypes[0]
	}
	return nil
}

func parseAmountMax(attrs map[string]string) int {
	for _, key := range []string{"max", "data-max", "data-max-amount", "data-cc-max-amount", "aria-valuemax"} {
		v := strings.TrimSpace(attrs[key])
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func eventIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// ...-21219007/ or ...-21219007
	re := regexp.MustCompile(`-(\d{6,})(?:/|$|\?)`)
	if m := re.FindStringSubmatch(u.Path); len(m) == 2 {
		return m[1]
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "-")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		last = strings.TrimSuffix(last, "/")
		ok := true
		for _, r := range last {
			if !unicode.IsDigit(r) {
				ok = false
				break
			}
		}
		if ok {
			return last
		}
	}
	return ""
}
