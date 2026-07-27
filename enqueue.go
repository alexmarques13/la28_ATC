package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func parseInqueueOptions(html string) map[string]any {
	out := map[string]any{}
	patterns := []struct {
		key string
		pat string
	}{
		{"queueId", `queueId:\s*'([0-9a-fA-F-]{36})'`},
		{"customerId", `customerId:\s*'([^']+)'`},
		{"eventId", `eventId:\s*'([^']+)'`},
		{"challengeApiChecksumHash", `challengeApiChecksumHash:\s*'((?:\\'|[^'])*)'`},
		{"layout", `layout:\s*'((?:\\'|[^'])*)'`},
	}
	for _, p := range patterns {
		m := regexp.MustCompile(p.pat).FindStringSubmatch(html)
		if m == nil {
			continue
		}
		val := m[1]
		if p.key == "challengeApiChecksumHash" || p.key == "layout" {
			val = softblockUnescape(val)
		}
		out[p.key] = val
	}
	if m := regexp.MustCompile(`challengesIssuedByReason:\s*(\d+)`).FindStringSubmatch(html); m != nil {
		n, _ := strconv.Atoi(m[1])
		out["challengesIssuedByReason"] = n
	}
	if m := regexp.MustCompile(`showAkamaiBotManagerHeaderVerificationChallenge:\s*(true|false)`).FindStringSubmatch(html); m != nil {
		out["showAkamaiBotManagerHeaderVerificationChallenge"] = m[1] == "true"
	}
	if m := regexp.MustCompile(`challenges:\s*(\[[^\]]*\])`).FindStringSubmatch(html); m != nil {
		var challenges []map[string]any
		if json.Unmarshal([]byte(m[1]), &challenges) == nil {
			out["challenges"] = challenges
		} else {
			out["challenges"] = []map[string]any{}
		}
	}
	return out
}

func solveAkamaiBMHeader(session *Session, cfg *SoftblockConfig, reason int, referer string) (string, error) {
	u := fmt.Sprintf("%s%s/spa-api/validator/%s/%s", baseURL, cfg.QueuePathPrefix, cfg.CustomerID, cfg.EventID)
	ref := referer
	if ref == "" {
		ref = baseURL + cfg.QueuePathPrefix + "/view"
	}
	hdrs := chromeHeaders(map[string]string{
		"accept":                     "*/*",
		"referer":                    ref,
		"x-queueit-challange-reason": strconv.Itoa(reason),
	}, "xhr")
	filtered := make([]headerKV, 0, len(hdrs))
	for _, kv := range hdrs {
		lk := strings.ToLower(kv.Key)
		if lk == "content-type" || lk == "x-requested-with" {
			continue
		}
		filtered = append(filtered, kv)
	}
	r, err := session.do("POST", u, filtered, strings.NewReader(""), 0)
	if err != nil {
		return "", err
	}
	preview := r.Body
	if len(preview) > 240 {
		preview = preview[:240]
	}
	fmt.Printf("[akamai-bm] validator %d %s\n", r.StatusCode, preview)
	if r.StatusCode != 200 {
		p2 := r.Body
		if len(p2) > 400 {
			p2 = p2[:400]
		}
		return "", fmt.Errorf("Akamai BM validator failed: %d %s", r.StatusCode, p2)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(r.Body), &data); err != nil {
		return "", err
	}
	value, _ := data["value"].(string)
	if value == "" {
		p2 := r.Body
		if len(p2) > 300 {
			p2 = p2[:300]
		}
		return "", fmt.Errorf("Akamai BM validator missing value: %s", p2)
	}
	fmt.Printf("[akamai-bm] QueueitEnqueueToken len=%d\n", len(value))
	return value, nil
}

func mergeQPN(cfg *SoftblockConfig) map[string]string {
	qpn := map[string]string{}
	for k, v := range defaultQPN {
		qpn[k] = v
	}
	for k, v := range cfg.QueryParameterNames {
		qpn[k] = v
	}
	return qpn
}

func enqueue(
	session *Session,
	cfg *SoftblockConfig,
	enqueueToken, softblockSCV, queueitEnqueueToken, referer, bodyReferrer string,
	challengeSessions []any,
) (map[string]any, error) {
	if challengeSessions == nil {
		challengeSessions = []any{}
	}
	qpn := mergeQPN(cfg)
	pairs := [][2]string{
		{qpn["CultureId"], cfg.Culture},
		{qpn["LayoutName"], cfg.Layout},
		{qpn["TargetUrl"], cfg.TargetURL},
	}
	if enqueueToken != "" {
		pairs = append(pairs, [2]string{qpn["EnqueueToken"], enqueueToken})
	}
	if softblockSCV != "" {
		pairs = append(pairs, [2]string{qpn["SoftBlockCaptchaSession"], softblockSCV})
	}
	qs := encodeQuery(pairs)
	u := fmt.Sprintf("%s%s/spa-api/queue/%s/%s/enqueue?%s",
		baseURL, cfg.QueuePathPrefix, cfg.CustomerID, cfg.EventID, qs)

	body := map[string]any{
		"challengeSessions":    challengeSessions,
		"layoutName":           cfg.Layout,
		"customUrlParams":      "",
		"targetUrl":            cfg.TargetURL,
		"CustomDataEnqueue":    nil,
		"QueueitEnqueueToken":  nil,
		"Referrer":             bodyReferrer,
	}
	if queueitEnqueueToken != "" {
		body["QueueitEnqueueToken"] = queueitEnqueueToken
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	hdrs := chromeHeaders(map[string]string{
		"accept":                   "application/json, text/javascript, */*; q=0.01",
		"content-type":             "application/json",
		"origin":                   baseURL,
		"referer":                  referer,
		"sec-fetch-dest":           "empty",
		"sec-fetch-mode":           "cors",
		"sec-fetch-site":           "same-origin",
		"x-requested-with":         "XMLHttpRequest",
		"x-queueit-qpage-referral": bodyReferrer,
	}, "xhr")

	r, err := session.do("POST", u, hdrs, strings.NewReader(string(bodyJSON)), 0)
	if err != nil {
		return nil, err
	}
	preview := r.Body
	if len(preview) > 300 {
		preview = preview[:300]
	}
	fmt.Printf("[enqueue] %d %s\n", r.StatusCode, preview)
	uprev := u
	if len(uprev) > 220 {
		uprev = uprev[:220]
	}
	fmt.Printf("[enqueue] url=%s...\n", uprev)

	if r.StatusCode == 403 || r.StatusCode == 429 {
		loc := headerGet(r.Header, "Location")
		fmt.Printf("[enqueue] restricted Location=%s\n", loc)
		interesting := map[string]string{}
		for _, k := range []string{
			"Location", "location", "x-queueit-redirectreason", "x-queueit-q", "server", "cf-ray",
		} {
			if v := headerGet(r.Header, k); v != "" {
				interesting[k] = v
			}
		}
		if len(interesting) > 0 {
			fmt.Printf("[enqueue] headers=%v\n", interesting)
		}
		return map[string]any{
			"redirectUrl": loc,
			"restricted":  true,
			"httpStatus":  r.StatusCode,
			"headers":     interesting,
		}, nil
	}
	if r.StatusCode != 200 && r.StatusCode != 201 {
		p2 := r.Body
		if len(p2) > 400 {
			p2 = p2[:400]
		}
		return nil, fmt.Errorf("enqueue failed: %d %s", r.StatusCode, p2)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(r.Body), &data); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeEnqueueTokenMeta(token string) map[string]any {
	if token == "" {
		return map[string]any{}
	}
	parts := strings.Split(token, ".")
	if len(parts) == 0 {
		return map[string]any{}
	}
	header := parts[0]
	pad := (4 - len(header)%4) % 4
	header += strings.Repeat("=", pad)
	raw, err := base64.URLEncoding.DecodeString(header)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return map[string]any{}
		}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return map[string]any{}
	}
	return out
}

func enqueueTokenTTLMs(token string) *int64 {
	meta := decodeEnqueueTokenMeta(token)
	expF, ok := meta["exp"].(float64)
	if !ok || expF == 0 {
		return nil
	}
	exp := int64(expF)
	nowMs := time.Now().UnixMilli()
	if exp < 10_000_000_000 {
		exp *= 1000
	}
	ttl := exp - nowMs
	return &ttl
}

func enqueueTokenIP(token string) any {
	ip := decodeEnqueueTokenMeta(token)["ip"]
	if ip == nil {
		return nil
	}
	return fmt.Sprint(ip)
}

func extractEnqueueToken(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"enqueuetoken", "enqueueToken"} {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func pollStatus(session *Session, cfg *SoftblockConfig, queueID, enqueueToken string, interval, maxWait float64) (map[string]any, error) {
	if interval <= 0 {
		interval = 2.0
	}
	if maxWait <= 0 {
		maxWait = 3600.0
	}
	deadline := time.Now().Add(time.Duration(maxWait * float64(time.Second)))
	layoutVersion := 0
	qpn := mergeQPN(cfg)

	for time.Now().Before(deadline) {
		pairs := [][2]string{
			{qpn["CultureId"], cfg.Culture},
			{qpn["LayoutName"], cfg.Layout},
			{qpn["TargetUrl"], cfg.TargetURL},
		}
		if enqueueToken != "" {
			pairs = append(pairs, [2]string{qpn["EnqueueToken"], enqueueToken})
		}
		qs := encodeQuery(pairs)
		u := fmt.Sprintf("%s%s/spa-api/queue/%s/%s/%s/status?%s",
			baseURL, cfg.QueuePathPrefix, cfg.CustomerID, cfg.EventID, queueID, qs)
		body := map[string]any{
			"targetUrl":              cfg.TargetURL,
			"customUrlParams":        "",
			"layoutVersion":          layoutVersion,
			"layoutName":             cfg.Layout,
			"isClientRedayToRedirect": true,
			"isBeforeOrIdle":         false,
		}
		bodyJSON, _ := json.Marshal(body)
		hdrs := chromeHeaders(map[string]string{
			"accept":          "application/json, text/javascript, */*; q=0.01",
			"content-type":    "application/json",
			"origin":          baseURL,
			"referer":         baseURL + cfg.QueuePathPrefix + "/",
			"sec-fetch-dest":  "empty",
			"sec-fetch-mode":  "cors",
			"sec-fetch-site":  "same-origin",
			"x-requested-with": "XMLHttpRequest",
		}, "xhr")

		r, err := session.do("POST", u, hdrs, strings.NewReader(string(bodyJSON)), 0)
		if err != nil {
			fmt.Printf("[status] err %v\n", err)
			time.Sleep(time.Duration(interval * float64(time.Second)))
			continue
		}
		if r.StatusCode == 403 || r.StatusCode == 429 {
			loc := headerGet(r.Header, "Location")
			fmt.Printf("[status] %d Location=%s\n", r.StatusCode, loc)
			if loc != "" {
				return map[string]any{"redirectUrl": loc, "httpStatus": r.StatusCode}, nil
			}
			time.Sleep(time.Duration(interval * float64(time.Second)))
			continue
		}
		if r.StatusCode != 200 {
			preview := r.Body
			if len(preview) > 200 {
				preview = preview[:200]
			}
			fmt.Printf("[status] %d %s\n", r.StatusCode, preview)
			time.Sleep(time.Duration(interval * float64(time.Second)))
			continue
		}

		var data map[string]any
		if r.Body != "" {
			_ = json.Unmarshal([]byte(r.Body), &data)
		}
		if data == nil {
			data = map[string]any{}
		}
		if redir, ok := data["redirectUrl"].(string); ok && redir != "" {
			fmt.Printf("[status] redirect isTarget=%v url=%s\n",
				data["isRedirectToTarget"], truncate(redir, 160))
			return data, nil
		}
		ticket, _ := data["ticket"].(map[string]any)
		fmt.Printf("[status] forecast=%v q#=%v ahead=%v progress=%v\n",
			data["forecastStatus"], ticket["queueNumber"], ticket["usersInLineAheadOfYou"], ticket["progress"])
		if lv, ok := data["layoutVersion"]; ok && lv != nil {
			switch t := lv.(type) {
			case float64:
				layoutVersion = int(t)
			case int:
				layoutVersion = t
			}
		}
		if ui, ok := data["updateInterval"].(float64); ok && ui > 0 {
			interval = ui / 1000.0
		}
		if interval < 1.0 {
			interval = 1.0
		}
		if interval > 30.0 {
			interval = 30.0
		}
		time.Sleep(time.Duration(interval * float64(time.Second)))
	}
	return nil, fmt.Errorf("queue status poll timed out")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
