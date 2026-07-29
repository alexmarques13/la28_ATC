package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Hyper-Solutions/hyper-sdk-go/v2"
)

var defaultQPN = map[string]string{
	"SoftBlockCaptchaSession": "scv",
	"CultureId":               "cid",
	"LayoutName":              "l",
	"TargetUrl":               "t",
	"QueueItEnqueueToken":     "qet",
	"EnqueueToken":            "enqueuetoken",
}

// SoftblockConfig is Queue-it's SoftblockViewModel subset.
type SoftblockConfig struct {
	CustomerID                string
	EventID                   string
	ChallengeVerifyEndpoint   string
	ChallengeAPIChecksumHash  string
	InqueueURLEncoded         string
	Challenges                []map[string]any
	ChallengesIssuedByReason  int
	RuleID                    string
	RuleName                  string
	Culture                   string
	QueuePathPrefix           string
	TargetURL                 string
	Layout                    string
	QueryParameterNames       map[string]string
}

func (c *SoftblockConfig) inqueueURL() string {
	u, err := url.QueryUnescape(c.InqueueURLEncoded)
	if err != nil {
		return c.InqueueURLEncoded
	}
	return u
}

func softblockUnescape(s string) string {
	// Prefer strconv.Unquote for common escapes; also handle \'.
	s = strings.ReplaceAll(s, `\'`, `'`)
	unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`)
	if err != nil {
		return s
	}
	return unquoted
}

func softblockField(html, name string) (string, error) {
	re := regexp.MustCompile(name + `:\s*'((?:\\'|[^'])*)'`)
	m := re.FindStringSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("softblock field missing: %s", name)
	}
	return softblockUnescape(m[1]), nil
}

func parseSoftblockConfig(html string) (*SoftblockConfig, error) {
	fieldInt := func(name string) int {
		re := regexp.MustCompile(name + `:\s*(-?\d+)`)
		m := re.FindStringSubmatch(html)
		if m == nil {
			return 0
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}

	var challenges []map[string]any
	if m := regexp.MustCompile(`challenges:\s*(\[[^\]]*\])`).FindStringSubmatch(html); m != nil {
		_ = json.Unmarshal([]byte(m[1]), &challenges)
	}
	qpn := map[string]string{}
	for k, v := range defaultQPN {
		qpn[k] = v
	}
	if m := regexp.MustCompile(`queryParameterNames:\s*(\{[^}]+\})`).FindStringSubmatch(html); m != nil {
		var parsed map[string]string
		if json.Unmarshal([]byte(m[1]), &parsed) == nil {
			for k, v := range parsed {
				qpn[k] = v
			}
		}
	}

	target := ""
	if tm := regexp.MustCompile(`targetUrl:\s*decodeURIComponent\('([^']+)'\)`).FindStringSubmatch(html); tm != nil {
		target, _ = url.QueryUnescape(tm[1])
	} else if strings.Contains(html, "targetUrl:") {
		if t, err := softblockField(html, "targetUrl"); err == nil {
			target = t
		}
	}

	must := func(name string) (string, error) { return softblockField(html, name) }
	customerID, err := must("customerId")
	if err != nil {
		return nil, err
	}
	eventID, err := must("eventId")
	if err != nil {
		return nil, err
	}
	challengeVerify, err := must("challengeVerifyEndpoint")
	if err != nil {
		return nil, err
	}
	challengeHash, err := must("challengeApiChecksumHash")
	if err != nil {
		return nil, err
	}
	inqueueEnc, err := must("inqueueUrl")
	if err != nil {
		return nil, err
	}
	ruleID, err := must("ruleId")
	if err != nil {
		return nil, err
	}
	ruleName, err := must("ruleName")
	if err != nil {
		return nil, err
	}
	culture, err := must("culture")
	if err != nil {
		return nil, err
	}
	qpp, err := must("queuePathPrefix")
	if err != nil {
		return nil, err
	}
	layout, err := must("layout")
	if err != nil {
		return nil, err
	}
	if qpp == "" {
		qpp = "/queue"
	}
	if target == "" {
		target = defaultTarget
	}

	return &SoftblockConfig{
		CustomerID:               customerID,
		EventID:                  eventID,
		ChallengeVerifyEndpoint:  challengeVerify,
		ChallengeAPIChecksumHash: challengeHash,
		InqueueURLEncoded:        inqueueEnc,
		Challenges:               challenges,
		ChallengesIssuedByReason: fieldInt("challengesIssuedByReason"),
		RuleID:                   ruleID,
		RuleName:                 ruleName,
		Culture:                  culture,
		QueuePathPrefix:          qpp,
		TargetURL:                target,
		Layout:                   layout,
		QueryParameterNames:      qpn,
	}, nil
}

func challengeHeaders(cfg *SoftblockConfig, forBotdetect bool) map[string]string {
	if forBotdetect {
		return map[string]string{
			"x-queueit-challange-CustomerId": cfg.CustomerID,
			"x-queueit-challange-EventId":    cfg.EventID,
			"x-queueit-challange-Hash":       cfg.ChallengeAPIChecksumHash,
			"x-queueit-challange-reason":     strconv.Itoa(cfg.ChallengesIssuedByReason),
			"x-queueit-challange-ruleid":     cfg.RuleID,
			"x-queueit-challange-rulename":   cfg.RuleName,
		}
	}
	return map[string]string{
		"x-queueit-challange-customerId": cfg.CustomerID,
		"x-queueit-challange-eventId":    cfg.EventID,
		"x-queueit-challange-hash":       cfg.ChallengeAPIChecksumHash,
	}
}

func isSoftblockPage(u, html string) bool {
	return strings.Contains(strings.ToLower(u), "softblock") ||
		strings.Contains(html, "SoftblockViewModel") ||
		strings.Contains(html, `data-pageid="softblock"`)
}

func isInqueueView(u, html string) bool {
	lu := strings.ToLower(u)
	return strings.Contains(lu, "/queue/view") ||
		strings.Contains(html, "InQueueViewModel") ||
		strings.Contains(html, `data-pageid="queue"`)
}

func softblockToInqueue(session *Session, cfg *SoftblockConfig, sessionInfo any, softblockReferer string) (html, viewURL string, err error) {
	raw, err := json.Marshal(sessionInfo)
	if err != nil {
		return "", "", err
	}
	scv := quoteLikePython(string(raw))
	u := cfg.inqueueURL()
	if !strings.HasPrefix(u, "http") {
		u = mustJoinURL(baseURL, u)
	}
	join := "?"
	if strings.Contains(u, "?") {
		join = "&"
	}
	dest := u + join + "scv=" + scv
	ref := softblockReferer
	if ref == "" {
		ref = baseURL + cfg.QueuePathPrefix + "/softblock/"
	}
	preview := dest
	if len(preview) > 180 {
		preview = preview[:180]
	}
	fmt.Printf("[softblock] redirect -> %s...\n", preview)

	r, err := session.do("GET", dest, chromeHeaders(map[string]string{
		"referer":        ref,
		"sec-fetch-site": "same-origin",
	}, "navigate"), nil, 0)
	if err != nil {
		return "", "", err
	}
	fmt.Printf("[inqueue] landed %d %s len=%d\n", r.StatusCode, r.URL, len(r.Body))
	return r.Body, r.URL, nil
}

func clearSoftblockOnce(
	session *Session,
	cfg *SoftblockConfig,
	hyperSession *hyper.Session,
	ip, twocaptchaKey, target, softblockRef, softblockHTML string,
) (*SoftblockConfig, any, string, string, string, string, error) {
	names := challengeNames(cfg.Challenges)
	if len(names) > 0 && !containsStr(names, "BotDetect") {
		return nil, nil, "", "", "", "", fmt.Errorf("unsupported softblock challenges: %v", names)
	}

	preview := softblockRef
	if len(preview) > 120 {
		preview = preview[:120]
	}
	fmt.Printf("[akamai] Softblock Hyper sensor -> %s\n", preview)
	htmlPtr := &softblockHTML
	updated, err := postHyperSensorOnce(session, hyperSession, softblockRef, ip, htmlPtr)
	if err != nil {
		return nil, nil, "", "", "", "", err
	}
	softblockHTML = updated
	if strings.Contains(softblockHTML, "SoftblockViewModel") {
		if fresh, err := parseSoftblockConfig(softblockHTML); err == nil {
			cfg = fresh
		}
	}

	sessionInfo, err := solveBotDetect(session, cfg, twocaptchaKey, 3)
	if err != nil {
		return nil, nil, "", "", "", "", err
	}
	siJSON, _ := json.Marshal(sessionInfo)
	siPrev := string(siJSON)
	if len(siPrev) > 300 {
		siPrev = siPrev[:300]
	}
	fmt.Printf("[softblock] sessionInfo=%s\n", siPrev)

	enqueueToken := extractEnqueueToken(cfg.inqueueURL())
	if enqueueToken == "" {
		enqueueToken = extractEnqueueToken(softblockRef)
	}
	ttl := enqueueTokenTTLMs(enqueueToken)
	fmt.Printf("[token] post-captcha ttl_ms=%v token_ip=%v\n", ttlString(ttl), enqueueTokenIP(enqueueToken))

	if ttl == nil || *ttl < 8000 {
		var tok string
		tok, cfg, softblockRef, err = refreshSoftblockEnqueueToken(session, target, cfg)
		if err != nil {
			return nil, nil, "", "", "", "", err
		}
		enqueueToken = tok
		fmt.Printf("[token] refreshed ttl_ms=%v token_ip=%v\n",
			ttlString(enqueueTokenTTLMs(enqueueToken)), enqueueTokenIP(enqueueToken))
		if !strings.Contains(softblockRef, "softblock") {
			q := [][2]string{
				{"c", cfg.CustomerID},
				{"e", cfg.EventID},
				{"t", orDefault(cfg.TargetURL, target)},
			}
			if enqueueToken != "" {
				q = append(q, [2]string{"enqueuetoken", enqueueToken})
			}
			q = append(q, [2]string{"rticr", "2"})
			softblockRef = baseURL + cfg.QueuePathPrefix + "/softblock/?" + encodeQuery(q)
		}
	}

	inqueueHTML, viewRef, err := softblockToInqueue(session, cfg, sessionInfo, softblockRef)
	if err != nil {
		return nil, nil, "", "", "", "", err
	}
	if !isInqueueView(viewRef, inqueueHTML) || isSoftblockPage(viewRef, inqueueHTML) {
		prev := viewRef
		if len(prev) > 200 {
			prev = prev[:200]
		}
		return nil, nil, "", "", "", "", fmt.Errorf("Softblock not cleared on first pass: %s", prev)
	}

	prev := viewRef
	if len(prev) > 160 {
		prev = prev[:160]
	}
	fmt.Printf("[softblock] cleared -> %s\n", prev)
	if t := extractEnqueueToken(viewRef); t != "" {
		enqueueToken = t
	}
	return cfg, sessionInfo, inqueueHTML, viewRef, enqueueToken, softblockRef, nil
}

func challengeNames(challenges []map[string]any) []string {
	out := make([]string, 0, len(challenges))
	for _, c := range challenges {
		if n, ok := c["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func ttlString(ttl *int64) any {
	if ttl == nil {
		return nil
	}
	return *ttl
}

func refreshSoftblockEnqueueToken(session *Session, target string, cfg *SoftblockConfig) (string, *SoftblockConfig, string, error) {
	softblockFallback := fmt.Sprintf(
		"%s%s/softblock/?c=%s&e=%s&t=%s&rticr=2",
		baseURL, cfg.QueuePathPrefix,
		quoteLikePython(cfg.CustomerID),
		quoteLikePython(cfg.EventID),
		quoteLikePython(orDefault(cfg.TargetURL, target)),
	)
	r, err := session.do("GET", target, chromeHeaders(map[string]string{
		"referer":        softblockFallback,
		"sec-fetch-site": "same-origin",
	}, "navigate"), nil, 0)
	if err != nil {
		return "", cfg, "", err
	}
	softblockURL := r.URL
	fmt.Printf("[token-refresh] %d %s\n", r.StatusCode, softblockURL)
	if !strings.Contains(r.Body, "SoftblockViewModel") && !strings.Contains(softblockURL, "softblock") {
		return extractEnqueueToken(softblockURL), cfg, softblockURL, nil
	}
	fresh, err := parseSoftblockConfig(r.Body)
	if err != nil {
		return extractEnqueueToken(softblockURL), cfg, softblockURL, nil
	}
	tok := extractEnqueueToken(fresh.inqueueURL())
	if tok == "" {
		tok = extractEnqueueToken(softblockURL)
	}
	fmt.Printf("[token-refresh] new token ttl_ms=%v\n", ttlString(enqueueTokenTTLMs(tok)))
	return tok, fresh, softblockURL, nil
}
