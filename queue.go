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
	reXSRF             = regexp.MustCompile(`xsrfToken:\s*"([^"]+)"`)
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
	verify := pick(reChallengeVerify, html, queuePathPrefix+"/challengeapi/web/la28tgigy260324/verify")

	return &QueueConfig{
		CustomerID:               pick(reCustomerID, html, ""),
		EventID:                  pick(reEventID, html, ""),
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

func (c *Client) enterQueuePage() (*QueueConfig, error) {
	entryURL := resolveQueueEntryURL(c.TargetURL)
	if entryURL != c.TargetURL {
		fmt.Printf("[queue] resolved entry %s -> %s\n", c.TargetURL, entryURL)
		c.TargetURL = entryURL
	}
	fmt.Printf("[queue] requesting %s\n", entryURL)

	resp, err := c.get(entryURL, c.navHeaders(""), true)
	if err != nil {
		return nil, err
	}
	queueURL := resp.URL
	html := resp.Text
	fmt.Printf("[queue] landed on %s\n", queueURL)

	if !strings.Contains(queueURL, "/queue/view") && !strings.Contains(strings.ToLower(queueURL), "queueittoken") {
		if err := c.solveAkamaiForURL(queueURL, html, true); err != nil {
			return nil, err
		}
		resp, err = c.get(entryURL, c.navHeaders(""), true)
		if err != nil {
			return nil, err
		}
		queueURL = resp.URL
		html = resp.Text
		fmt.Printf("[queue] retry landed on %s\n", queueURL)
	}

	if strings.Contains(strings.ToLower(queueURL), "queueittoken") {
		fmt.Println("[queue] already passed queue (queueittoken in URL)")
		return c.parseQueuePage(html, queueURL)
	}

	if !strings.Contains(queueURL, "/queue/view") {
		return nil, fmt.Errorf("expected Queue-it page, got %s", queueURL)
	}

	cfg, err := c.parseQueuePage(html, queueURL)
	if err != nil {
		return nil, err
	}
	if cfg.EnqueueToken == "" {
		return nil, fmt.Errorf("missing enqueue token on queue page")
	}
	c.queueConfig = cfg
	if err := c.solveAkamaiForURL(queueURL, html, true); err != nil {
		return nil, err
	}
	return cfg, nil
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
		"challengeSessions":    []any{},
		"layoutName":           cfg.LayoutName,
		"customUrlParams":      "",
		"targetUrl":            cfg.TargetURL,
		"CustomDataEnqueue":    nil,
		"QueueitEnqueueToken":  queueitEnqueueToken,
		"Referrer":             cfg.TargetURL,
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
		retryable := strings.Contains(msg, "403") || strings.Contains(msg, "validator failed")
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
	referer := redirectURL
	if c.queueConfig != nil && c.queueConfig.QueueURL != "" {
		referer = c.queueConfig.QueueURL
	}
	resp, err := c.get(redirectURL, c.navHeaders(referer), true)
	if err != nil {
		return err
	}
	fmt.Printf("[queue] followed redirect -> %s (%d)\n", resp.URL, resp.StatusCode)
	u := mustParseURL(baseURL)
	var queueCookies []string
	for _, ck := range c.http.GetCookies(u) {
		if strings.HasPrefix(strings.ToLower(ck.Name), "queueit") {
			queueCookies = append(queueCookies, ck.Name)
		}
	}
	fmt.Printf("[queue] queue cookies: %v\n", queueCookies)
	return nil
}

func (c *Client) JoinAndWait(maxPolls int) (*QueueStatus, error) {
	cfg, err := c.enterQueuePage()
	if err != nil {
		return nil, err
	}
	if strings.Contains(strings.ToLower(cfg.QueueURL), "queueittoken") {
		return &QueueStatus{RedirectURL: cfg.QueueURL}, nil
	}

	enqueueResult, err := c.challengeAndEnqueue(cfg)
	if err != nil {
		return nil, err
	}

	if redir, ok := enqueueResult["redirectUrl"].(string); ok && redir != "" {
		fmt.Printf("[queue] immediate redirect: %s\n", redir)
		_ = c.followRedirect(redir)
		raw, _ := json.Marshal(enqueueResult)
		return &QueueStatus{RedirectURL: redir, Raw: raw}, nil
	}

	queueID, _ := enqueueResult["queueId"].(string)
	if queueID == "" {
		return nil, fmt.Errorf("enqueue returned no queueId: %v", enqueueResult)
	}
	fmt.Printf("[queue] joined queue %s\n", queueID)

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
