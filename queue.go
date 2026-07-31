package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reCustomerID       = regexp.MustCompile(`customerId:\s*'([^']+)'`)
	reEventID          = regexp.MustCompile(`eventId:\s*'([^']+)'`)
	reLayout           = regexp.MustCompile(`layout:\s*'([^']+)'`)
	reLayoutVersion    = regexp.MustCompile(`layoutVersion:(\d+)`)
	reChallengeHash    = regexp.MustCompile(`challengeApiChecksumHash:\s*'([^']+)'`)
	reChallengeVerify  = regexp.MustCompile(`challengeVerifyEndpoint:\s*'([^']+)'`)
	reChallengesReason = regexp.MustCompile(`challengesIssuedByReason:\s*(\d+)`)
	reXSRF             = regexp.MustCompile(`(?:xsrfToken|"xsrfToken")\s*:\s*"([^"]+)"`)
)

func pick(re *regexp.Regexp, html, def string) string {
	m := re.FindStringSubmatch(html)
	if len(m) > 1 {
		return m[1]
	}
	return def
}

func (c *Client) parseQueuePage(html, queueURL string) (*QueueConfig, error) {
	u := mustParseURL(queueURL)
	q := u.Query()
	target := q.Get("t")
	if target == "" {
		target = c.TargetURL
	}
	layoutVersion, _ := strconv.Atoi(pick(reLayoutVersion, html, "0"))
	reason, _ := strconv.Atoi(pick(reChallengesReason, html, "1"))
	customer := pick(reCustomerID, html, q.Get("c"))
	eventID := pick(reEventID, html, q.Get("e"))
	verifyDefault := queuePathPrefix + "/challengeapi/" + customer + "/" + eventID + "/verify"
	verify := pick(reChallengeVerify, html, verifyDefault)

	return &QueueConfig{
		CustomerID:               customer,
		EventID:                  eventID,
		LayoutName:               pick(reLayout, html, ""),
		LayoutVersion:            layoutVersion,
		TargetURL:                target,
		EnqueueToken:             q.Get("enqueuetoken"),
		ChallengeChecksum:        pick(reChallengeHash, html, ""),
		ChallengeVerifyEndpoint:  verify,
		ChallengesIssuedByReason: reason,
		Culture:                  "en-US",
		QueueURL:                 queueURL,
	}, nil
}

func isQueueitURL(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "/queue/") ||
		strings.Contains(low, "queueittoken") ||
		strings.Contains(low, "queue-it.net")
}

func hasQueueitToken(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "queueittoken=")
}

func isSafetyNetURL(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "rt_safetynet") || strings.Contains(low, "safetynet")
}

// softQueueToView rewrites /queue/?… → /queue/view?… so we land on the SPA
// waiting-room page instead of following SafetyNet redirect loops.
func softQueueToView(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "/queue" {
		u.Path = "/queue/view"
		return u.String()
	}
	return raw
}

func queueEventIDFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if e := u.Query().Get("e"); e != "" {
		return e
	}
	// queueittoken=e_<eventId>~ts_...
	tok := u.Query().Get("queueittoken")
	if tok == "" {
		return ""
	}
	if strings.HasPrefix(tok, "e_") {
		rest := strings.TrimPrefix(tok, "e_")
		if i := strings.IndexByte(rest, '~'); i > 0 {
			return rest[:i]
		}
	}
	return ""
}

func (c *Client) hasQueueAcceptedCookie(eventID string) bool {
	if eventID == "" {
		return false
	}
	u := mustParseURL(baseURL)
	suffix := strings.ToLower(eventID)
	for _, ck := range c.http.GetCookies(u) {
		name := strings.ToLower(ck.Name)
		if strings.HasPrefix(name, "queueitaccepted") && strings.Contains(name, suffix) && ck.Value != "" {
			return true
		}
	}
	return false
}

func (c *Client) enterQueuePage() (*QueueConfig, error) {
	return c.enterQueueForURL(c.TargetURL)
}

// enterQueueForURL hits entryURL and parses whichever Queue-it waiting room protects it.
func (c *Client) enterQueueForURL(entryURL string) (*QueueConfig, error) {
	entryURL = resolveQueueEntryURL(entryURL)
	fmt.Printf("[queue] requesting %s\n", entryURL)

	resp, err := c.get(entryURL, c.navHeaders(""), false)
	if err != nil {
		return nil, err
	}
	queueURL := resp.URL
	html := resp.Text

	if loc := headerGet(resp.Header, "Location"); loc != "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if !strings.HasPrefix(loc, "http") {
			base := mustParseURL(entryURL)
			loc = base.ResolveReference(mustParseURL(loc)).String()
		}
		fmt.Printf("[queue] redirect -> %s\n", truncate(loc, 140))
		return c.enterQueueForURLFromSoft(entryURL, loc)
	} else if !isQueueitURL(queueURL) {
		if err := c.solveAkamaiForURL(queueURL, html, true); err != nil {
			return nil, err
		}
		resp, err = c.get(entryURL, c.navHeaders(""), false)
		if err != nil {
			return nil, err
		}
		if loc := headerGet(resp.Header, "Location"); loc != "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
			if !strings.HasPrefix(loc, "http") {
				base := mustParseURL(entryURL)
				loc = base.ResolveReference(mustParseURL(loc)).String()
			}
			return c.enterQueueForURLFromSoft(entryURL, loc)
		}
		queueURL = resp.URL
		html = resp.Text
	}
	fmt.Printf("[queue] landed on %s (%d)\n", truncate(queueURL, 120), resp.StatusCode)

	if hasQueueitToken(queueURL) {
		fmt.Println("[queue] token present — redeeming cookie")
		if err := c.redeemQueueitAccess(entryURL, queueURL); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no queue required for %s (token redeemed)", entryURL)
	}
	if !isQueueitURL(queueURL) && !strings.Contains(html, "enqueuetoken") && !strings.Contains(html, "customerId:") {
		return nil, fmt.Errorf("no queue required for %s (landed on %s)", entryURL, queueURL)
	}

	// Ensure we parse from /queue/view HTML (enqueue token + layout).
	if !strings.Contains(queueURL, "/queue/view") {
		if q := softQueueToView(queueURL); q != queueURL {
			resp, err = c.get(q, c.navHeaders(entryURL), false)
			if err != nil {
				return nil, err
			}
			queueURL = resp.URL
			html = resp.Text
			fmt.Printf("[queue] settled on %s\n", truncate(queueURL, 120))
		}
	}
	if !strings.Contains(html, "customerId:") && !strings.Contains(html, "enqueuetoken") {
		// Need the body of the view page — follow one time if 302 to same view with cookies.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, fmt.Errorf("expected Queue-it view HTML, got redirect %s", queueURL)
		}
	}

	cfg, err := c.parseQueuePage(html, queueURL)
	if err != nil {
		return nil, err
	}
	// Prefer query enqueuetoken from soft URL if HTML parse missed it.
	if cfg.EnqueueToken == "" {
		if u, err := url.Parse(queueURL); err == nil {
			cfg.EnqueueToken = u.Query().Get("enqueuetoken")
		}
	}
	if cfg.TargetURL == "" {
		cfg.TargetURL = entryURL
	}
	if cfg.CustomerID == "" || cfg.EventID == "" {
		if u, err := url.Parse(queueURL); err == nil {
			if cfg.CustomerID == "" {
				cfg.CustomerID = u.Query().Get("c")
			}
			if cfg.EventID == "" {
				cfg.EventID = u.Query().Get("e")
			}
		}
	}
	if cfg.EnqueueToken == "" {
		return nil, fmt.Errorf("missing enqueue token on queue page")
	}
	if cfg.LayoutName == "" {
		// View HTML required for layout; try fetching view with shared query from soft URL.
		return nil, fmt.Errorf("missing layout on queue page (need /queue/view HTML)")
	}
	c.queueConfig = cfg
	if err := c.solveAkamaiForURL(queueURL, html, true); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Client) enterQueueForURLFromSoft(entryURL, softLoc string) (*QueueConfig, error) {
	fmt.Printf("[queue] soft gate %s\n", truncate(softLoc, 140))
	ev := queueEventIDFromURL(softLoc)
	if c.hasQueueAcceptedCookie(ev) {
		fmt.Printf("[queue] reusing QueueITAccepted for %s\n", ev)
		if err := c.redeemQueueitAccess(entryURL, softLoc); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("no queue required for %s (cookie accepted)", entryURL)
	}

	// Probe soft /queue/? once (no auto-follow — that loops with SafetyNet).
	softResp, err := c.get(softLoc, c.navHeaders(entryURL), false)
	if err != nil {
		return nil, err
	}
	var softLoc2 string
	if loc := headerGet(softResp.Header, "Location"); loc != "" && softResp.StatusCode >= 300 && softResp.StatusCode < 400 {
		if !strings.HasPrefix(loc, "http") {
			base := mustParseURL(softLoc)
			loc = base.ResolveReference(mustParseURL(loc)).String()
		}
		softLoc2 = loc
	}

	// Soft-gate token redeem (includes idle/SafetyNet soft passes). Browser does the same
	// when KnownUser issues an immediate queueittoken — do not reject SafetyNet here.
	tryRedeem := func(tokenURL string) bool {
		if u, err := url.Parse(tokenURL); err == nil {
			tok := u.Query().Get("queueittoken")
			rt := ""
			for _, p := range strings.Split(tok, "~") {
				if strings.HasPrefix(p, "rt_") {
					rt = p
					break
				}
			}
			fmt.Printf("[queue] trying token redeem rt=%s %s\n", rt, truncate(tokenURL, 100))
		} else {
			fmt.Printf("[queue] trying token redeem %s\n", truncate(tokenURL, 100))
		}
		if err := c.redeemQueueitAccess(entryURL, tokenURL); err != nil {
			fmt.Printf("[queue] token redeem incomplete: %v\n", err)
			return false
		}
		if !c.hasQueueAcceptedCookie(ev) && !c.hasQueueAcceptedCookie(queueEventIDFromURL(tokenURL)) {
			fmt.Println("[queue] token redeem did not set QueueITAccepted for this room — need waiting room")
			return false
		}
		return true
	}
	if softLoc2 != "" && hasQueueitToken(softLoc2) {
		if tryRedeem(softLoc2) {
			return nil, fmt.Errorf("no queue required for %s (token redeemed)", entryURL)
		}
	} else if hasQueueitToken(softResp.URL) {
		if tryRedeem(softResp.URL) {
			return nil, fmt.Errorf("no queue required for %s (token redeemed)", entryURL)
		}
	}

	// Real waiting room: /queue/view with the soft query (enqueuetoken, layout).
	viewURL := softQueueToView(softLoc)
	if softLoc2 != "" && strings.Contains(softLoc2, "/queue/view") {
		viewURL = softLoc2
	}
	fmt.Printf("[queue] using view page %s\n", truncate(viewURL, 140))
	resp, err := c.get(viewURL, c.navHeaders(entryURL), false)
	if err != nil {
		return nil, err
	}
	if loc := headerGet(resp.Header, "Location"); loc != "" && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if !strings.HasPrefix(loc, "http") {
			base := mustParseURL(viewURL)
			loc = base.ResolveReference(mustParseURL(loc)).String()
		}
		if hasQueueitToken(loc) {
			if tryRedeem(loc) {
				return nil, fmt.Errorf("no queue required for %s (token redeemed)", entryURL)
			}
			// Still need the SPA HTML — ignore and keep using view body if any.
		}
		if strings.Contains(loc, "/queue/view") {
			resp, err = c.get(loc, c.navHeaders(viewURL), false)
			if err != nil {
				return nil, err
			}
			viewURL = resp.URL
		}
	}
	if hasQueueitToken(resp.URL) {
		if tryRedeem(resp.URL) {
			return nil, fmt.Errorf("no queue required for %s (token redeemed)", entryURL)
		}
	}

	html := resp.Text
	// Soft /queue/view sometimes 302s; if body is empty, fetch view again after sensors.
	if !strings.Contains(html, "customerId:") && !strings.Contains(html, "layout:") {
		if err := c.solveAkamaiForURL(viewURL, html, false); err == nil {
			resp2, err := c.get(viewURL, c.navHeaders(entryURL), false)
			if err == nil && (strings.Contains(resp2.Text, "customerId:") || strings.Contains(resp2.Text, "layout:")) {
				resp = resp2
				html = resp2.Text
				viewURL = resp2.URL
			}
		}
	}

	cfg, err := c.parseQueuePage(html, viewURL)
	if err != nil {
		return nil, err
	}
	if u, err := url.Parse(softLoc); err == nil {
		if cfg.EnqueueToken == "" {
			cfg.EnqueueToken = u.Query().Get("enqueuetoken")
		}
		if cfg.CustomerID == "" {
			cfg.CustomerID = u.Query().Get("c")
		}
		if cfg.EventID == "" {
			cfg.EventID = u.Query().Get("e")
		}
	}
	if cfg.TargetURL == "" {
		cfg.TargetURL = entryURL
	}
	if cfg.EnqueueToken == "" || cfg.LayoutName == "" {
		return nil, fmt.Errorf("incomplete queue config from %s (token=%t layout=%q status=%d)", viewURL, cfg.EnqueueToken != "", cfg.LayoutName, resp.StatusCode)
	}
	c.queueConfig = cfg
	if err := c.solveAkamaiForURL(viewURL, html, true); err != nil {
		return nil, err
	}
	return cfg, nil
}

// redeemQueueitAccess performs a token/cookie redemption without redirect loops.
// Success requires QueueITAccepted for the queue event id (not merely a 302).
func (c *Client) redeemQueueitAccess(targetURL, tokenOrQueueURL string) error {
	wantEv := queueEventIDFromURL(tokenOrQueueURL)
	if wantEv == "" {
		wantEv = queueEventIDFromURL(targetURL)
	}

	current := tokenOrQueueURL
	referer := targetURL
	seen := map[string]bool{}
	for hop := 0; hop < 6; hop++ {
		if seen[current] {
			break
		}
		seen[current] = true

		resp, err := c.get(current, c.navHeaders(referer), false)
		if err != nil {
			return err
		}
		loc := headerGet(resp.Header, "Location")
		if loc != "" && !strings.HasPrefix(loc, "http") {
			base := mustParseURL(current)
			loc = base.ResolveReference(mustParseURL(loc)).String()
		}
		fmt.Printf("[queue] redeem hop %d → %d %s\n", hop+1, resp.StatusCode, truncate(current, 90))
		if loc != "" {
			fmt.Printf("[queue] redeem location → %s\n", truncate(loc, 100))
		}
		c.logQueueCookies()

		if c.hasQueueAcceptedCookie(wantEv) {
			break
		}
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}

		// Only stop after a token hop when this room's accepted cookie was set.
		// rt_safetynet often 302s to the event without setting QueueITAccepted-…_la28q2…
		if hasQueueitToken(current) && c.hasQueueAcceptedCookie(wantEv) {
			break
		}
		if strings.Contains(loc, "/queue/") && !hasQueueitToken(loc) {
			if c.hasQueueAcceptedCookie(queueEventIDFromURL(loc)) {
				break
			}
			next := softQueueToView(loc)
			if seen[next] {
				break
			}
			referer = current
			current = next
			continue
		}
		referer = current
		current = loc
	}

	clean := stripQueueitToken(targetURL)
	resp, err := c.get(clean, c.navHeaders(referer), false)
	if err != nil {
		return err
	}
	loc := headerGet(resp.Header, "Location")
	if loc != "" && !strings.HasPrefix(loc, "http") {
		base := mustParseURL(clean)
		loc = base.ResolveReference(mustParseURL(loc)).String()
	}

	if c.hasQueueAcceptedCookie(wantEv) && !isQueueitURL(loc) {
		fmt.Printf("[queue] access ok for %s\n", truncate(clean, 80))
		c.logQueueCookies()
		return nil
	}
	if isQueueitURL(loc) || (resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.Contains(strings.ToLower(loc), "queue")) {
		return fmt.Errorf("queue cookie not accepted yet (still gated, want=%s)", wantEv)
	}
	if !c.hasQueueAcceptedCookie(wantEv) {
		return fmt.Errorf("queue cookie not accepted yet (want=%s, no QueueITAccepted)", wantEv)
	}
	fmt.Printf("[queue] access ok for %s\n", truncate(clean, 80))
	c.logQueueCookies()
	return nil
}

func stripQueueitToken(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Del("queueittoken")
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) logQueueCookies() {
	u := mustParseURL(baseURL)
	var names []string
	for _, ck := range c.http.GetCookies(u) {
		if strings.HasPrefix(strings.ToLower(ck.Name), "queueit") {
			names = append(names, ck.Name)
		}
	}
	fmt.Printf("[queue] cookies: %v\n", names)
}

// resolveQueueEntryURL maps the public forceLogin homepage to the protected
// mycustomerdata path that actually triggers Queue-it, keeping query params.
func resolveQueueEntryURL(target string) string {
	u := mustParseURL(target)
	if u.Host == "" {
		return target
	}
	path := u.Path
	if path == "" || path == "/" {
		u.Path = "/mycustomerdata/"
		// Ensure forceLogin stays present for this waiting-room mode.
		q := u.Query()
		if q.Get("forceLogin") == "" && strings.Contains(strings.ToLower(target), "forcelogin") {
			q.Set("forceLogin", "true")
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	return target
}

func (c *Client) akamaiValidatorSolution(cfg *QueueConfig) (string, error) {
	validatorURL := fmt.Sprintf("%s%s/spa-api/validator/%s/%s",
		baseURL, queuePathPrefix, cfg.CustomerID, cfg.EventID)
	headers := c.apiHeaders(cfg.QueueURL, map[string]string{
		"X-Queueit-Challange-reason": strconv.Itoa(cfg.ChallengesIssuedByReason),
	})
	resp, err := c.post(validatorURL, headers, bytes.NewReader(nil), true)
	if err != nil {
		return "", err
	}
	fmt.Printf("[challenge] akamai validator: %d %s\n", resp.StatusCode, truncate(resp.Text, 300))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("akamai validator failed: %d %s", resp.StatusCode, truncate(resp.Text, 500))
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (c *Client) solveChallenges(cfg *QueueConfig) (string, error) {
	token, err := c.akamaiValidatorSolution(cfg)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("akamai validator returned no token")
	}
	fmt.Println("[challenge] akamai token acquired (used as QueueitEnqueueToken)")
	return token, nil
}

func (c *Client) enqueue(cfg *QueueConfig, queueitEnqueueToken string) (map[string]any, error) {
	params := url.Values{
		"cid":          {cfg.Culture},
		"l":            {cfg.LayoutName},
		"t":            {cfg.TargetURL},
		"enqueuetoken": {cfg.EnqueueToken},
	}
	enqueueURL := fmt.Sprintf("%s%s/spa-api/queue/%s/%s/enqueue?%s",
		baseURL, queuePathPrefix, cfg.CustomerID, cfg.EventID, params.Encode())

	body := map[string]any{
		"challengeSessions":   []any{},
		"layoutName":          cfg.LayoutName,
		"customUrlParams":     "",
		"targetUrl":           cfg.TargetURL,
		"CustomDataEnqueue":   nil,
		"QueueitEnqueueToken": queueitEnqueueToken,
		"Referrer":            cfg.TargetURL,
	}
	payload, _ := json.Marshal(body)
	headers := c.apiHeaders(cfg.QueueURL, map[string]string{
		"x-queueit-qpage-referral": cfg.TargetURL,
	})
	resp, err := c.post(enqueueURL, headers, bytes.NewReader(payload), true)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[enqueue] %d %s\n", resp.StatusCode, truncate(resp.Text, 500))
	if h := headerGet(resp.Header, "X-Queueit-Queueitem-V2"); h != "" {
		c.queueItemHeader = h
	}
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		// Persist body for debugging Akamai soft blocks.
		if len(resp.Body) > 0 && len(resp.Body) < 400 {
			fmt.Printf("[enqueue] body: %s\n", truncate(strings.ReplaceAll(resp.Text, "\n", " "), 300))
		}
		return nil, fmt.Errorf("enqueue failed: %d %s", resp.StatusCode, truncate(resp.Text, 800))
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	if data["redirectUrl"] != nil {
		return data, nil
	}
	if v, _ := data["challengeRequired"].(bool); v {
		return nil, fmt.Errorf("additional challenge required: %v", data)
	}
	if v, _ := data["challengeFailed"].(bool); v {
		return nil, fmt.Errorf("challenge failed at enqueue: %v", data)
	}
	return data, nil
}

func (c *Client) challengeAndEnqueue(cfg *QueueConfig) (map[string]any, error) {
	var lastErr error
	for attempt := 1; attempt <= maxEnqueueRetries; attempt++ {
		token, err := c.solveChallenges(cfg)
		if err != nil {
			lastErr = err
		} else {
			c.queueitEnqueueToken = token
			if err := c.refreshAkamai(cfg.QueueURL); err != nil {
				lastErr = err
			} else {
				data, err := c.enqueue(cfg, token)
				if err == nil {
					return data, nil
				}
				lastErr = err
			}
		}

		msg := ""
		if lastErr != nil {
			msg = lastErr.Error()
		}
		// A 403 means this session is burned, and re-solving in place just replays
		// the block. Surface it so the caller can start over on a new fingerprint.
		retryable := strings.Contains(msg, "validator failed") && !strings.Contains(msg, "failed: 403")
		if !retryable || attempt >= maxEnqueueRetries {
			return nil, lastErr
		}
		fmt.Printf("[enqueue] retry %d/%d after: %s\n", attempt, maxEnqueueRetries, truncate(msg, 120))
		time.Sleep(time.Duration(attempt) * time.Second)

		page, err := c.get(cfg.QueueURL, c.navHeaders(""), true)
		if err != nil {
			continue
		}
		// Full sensor re-solve on hard 403s; a single refresh is often not enough.
		_ = c.solveAkamaiForURL(page.URL, page.Text, true)
		if refreshed, err := c.parseQueuePage(page.Text, page.URL); err == nil && refreshed.EnqueueToken != "" {
			cfg.EnqueueToken = refreshed.EnqueueToken
			if refreshed.ChallengeChecksum != "" {
				cfg.ChallengeChecksum = refreshed.ChallengeChecksum
			}
			if refreshed.QueueURL != "" {
				cfg.QueueURL = refreshed.QueueURL
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("enqueue failed")
	}
	return nil, lastErr
}

func (c *Client) pollStatus(cfg *QueueConfig, queueID string) (*QueueStatus, error) {
	params := url.Values{
		"cid":                 {cfg.Culture},
		"l":                   {cfg.LayoutName},
		"t":                   {cfg.TargetURL},
		"enqueuetoken":        {cfg.EnqueueToken},
		"queueItEnqueueToken": {c.queueitEnqueueToken},
	}
	statusURL := fmt.Sprintf("%s%s/spa-api/queue/%s/%s/%s/status?%s",
		baseURL, queuePathPrefix, cfg.CustomerID, cfg.EventID, queueID, params.Encode())

	extra := map[string]string{}
	if c.queueItemHeader != "" {
		extra["X-Queueit-Queueitem-V2"] = c.queueItemHeader
	}
	headers := c.apiHeaders(cfg.QueueURL, extra)
	body := map[string]any{
		"targetUrl":               cfg.TargetURL,
		"customUrlParams":         "",
		"layoutVersion":           cfg.LayoutVersion,
		"layoutName":              cfg.LayoutName,
		"isClientRedayToRedirect": true,
		"isBeforeOrIdle":          false,
	}
	payload, _ := json.Marshal(body)
	resp, err := c.post(statusURL, headers, bytes.NewReader(payload), true)
	if err != nil {
		return nil, err
	}
	if h := headerGet(resp.Header, "X-Queueit-Queueitem-V2"); h != "" {
		c.queueItemHeader = h
	}

	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		if loc := headerGet(resp.Header, "Location"); loc != "" {
			return &QueueStatus{QueueID: queueID, RedirectURL: loc}, nil
		}
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status poll failed: %d %s", resp.StatusCode, truncate(resp.Text, 500))
	}

	var data map[string]any
	_ = json.Unmarshal(resp.Body, &data)
	raw, _ := json.Marshal(data)
	ticket, _ := data["ticket"].(map[string]any)

	status := &QueueStatus{
		QueueID: queueID,
		Raw:     raw,
	}
	if v, ok := data["pageId"].(string); ok {
		status.PageID = v
	}
	if v, ok := data["redirectUrl"].(string); ok {
		status.RedirectURL = v
	}
	if status.RedirectURL == "" {
		status.RedirectURL = headerGet(resp.Header, "Location")
	}
	if ticket != nil {
		if v, ok := ticket["expectedServiceTime"].(string); ok {
			status.ExpectedServiceTime = v
		}
		if v, ok := ticket["queuePaused"].(bool); ok {
			status.QueuePaused = v
		}
		if v, ok := ticket["progress"].(float64); ok {
			status.Progress = &v
		}
		if v, ok := ticket["queueNumber"].(float64); ok {
			n := int(v)
			status.QueueNumber = &n
		}
		if v, ok := ticket["usersInLineAheadOfYou"].(float64); ok {
			n := int(v)
			status.UsersAhead = &n
		}
	}
	return status, nil
}

func (c *Client) followRedirect(redirectURL string) error {
	target := c.TargetURL
	if c.queueConfig != nil && c.queueConfig.TargetURL != "" {
		target = c.queueConfig.TargetURL
	}
	// Token / exit URLs must be redeemed hop-by-hop so QueueITAccepted sticks
	// without bouncing event ↔ soft queue forever.
	if err := c.redeemQueueitAccess(target, redirectURL); err != nil {
		return err
	}
	fmt.Printf("[queue] followed redirect (cookie redeemed)\n")
	return nil
}

func (c *Client) JoinAndWait(maxPolls int) (*QueueStatus, error) {
	entry := resolveQueueEntryURL(c.TargetURL)
	if entry != c.TargetURL {
		fmt.Printf("[queue] resolved entry %s -> %s\n", c.TargetURL, entry)
		c.TargetURL = entry
	}
	return c.JoinAndWaitForURL(c.TargetURL, maxPolls)
}

// JoinAndWaitForURL passes whichever Queue-it room protects targetURL (login room,
// event room, etc.), then follows the redirect back to the protected page.
func (c *Client) JoinAndWaitForURL(targetURL string, maxPolls int) (*QueueStatus, error) {
	cfg, err := c.enterQueueForURL(targetURL)
	if err != nil {
		if strings.Contains(err.Error(), "no queue required") {
			fmt.Printf("[queue] %v\n", err)
			return &QueueStatus{RedirectURL: targetURL}, nil
		}
		return nil, err
	}
	if cfg == nil {
		return &QueueStatus{RedirectURL: targetURL}, nil
	}
	if hasQueueitToken(cfg.QueueURL) {
		_ = c.followRedirect(cfg.QueueURL)
		return &QueueStatus{RedirectURL: cfg.QueueURL}, nil
	}

	enqueueResult, err := c.challengeAndEnqueue(cfg)
	if err != nil {
		return nil, err
	}

	if redir, ok := enqueueResult["redirectUrl"].(string); ok && redir != "" {
		// Soft/SafetyNet immediate redirects are how KnownUser lets traffic through
		// when the room is idle — same path a browser soft-gate takes. Follow them.
		fmt.Printf("[queue] immediate redirect: %s\n", truncate(redir, 140))
		if isSafetyNetURL(redir) {
			fmt.Println("[queue] note: soft/SafetyNet pass — continuing (browser does this on idle rooms)")
		}
		_ = c.followRedirect(redir)
		raw, _ := json.Marshal(enqueueResult)
		return &QueueStatus{RedirectURL: redir, Raw: raw}, nil
	}

	queueID, _ := enqueueResult["queueId"].(string)
	if queueID == "" {
		return nil, fmt.Errorf("enqueue returned no queueId: %v", enqueueResult)
	}
	fmt.Printf("[queue] joined queue %s (event=%s)\n", queueID, cfg.EventID)

	polls := 0
	for {
		status, err := c.pollStatus(cfg, queueID)
		if err != nil {
			return nil, err
		}
		polls++

		if status.RedirectURL != "" {
			fmt.Printf("[queue] your turn! redirect: %s\n", status.RedirectURL)
			_ = c.followRedirect(status.RedirectURL)
			return status, nil
		}

		progressTxt := "—"
		if status.Progress != nil {
			progressTxt = fmt.Sprintf("%.0f%%", (*status.Progress)*100)
		}
		forecast := "—"
		var raw map[string]any
		_ = json.Unmarshal(status.Raw, &raw)
		if v, ok := raw["forecastStatus"].(string); ok && v != "" {
			forecast = v
		}
		eta := status.ExpectedServiceTime
		if eta == "" {
			eta = "—"
		}
		var number any = nil
		var ahead any = nil
		if status.QueueNumber != nil {
			number = *status.QueueNumber
		}
		if status.UsersAhead != nil {
			ahead = *status.UsersAhead
		}
		fmt.Printf("[queue] poll %d: progress=%s, page=%s, forecast=%s, position=%v, ahead=%v, eta=%s, paused=%v\n",
			polls, progressTxt, status.PageID, forecast, number, ahead, eta, status.QueuePaused)

		if maxPolls > 0 && polls >= maxPolls {
			fmt.Printf("[queue] reached max polls (%d), stopping\n", maxPolls)
			return status, nil
		}
		time.Sleep(c.PollInterval)
	}
}
