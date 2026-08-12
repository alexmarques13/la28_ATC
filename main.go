package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
	"github.com/joho/godotenv"
)


var tlsProfiles = []string{
	"chrome146",
	"chrome144",
}

var acceptLangs = []string{
	"en-US,en;q=0.9",
	"en-US,en;q=0.8",
	"en-US,en;q=0.9,es;q=0.8",
	"en-US,en;q=0.9,fr;q=0.5",
}

var screens = []string{
	"1920 x 1080",
	"2560 x 1440",
	"1536 x 864",
	"1440 x 900",
	"1366 x 768",
	"1680 x 1050",
	"2048 x 1152",
}

var brandTemplates = []string{
	`"Google Chrome";v="{v}", "Chromium";v="{v}", "Not)A;Brand";v="24"`,
	`"Chromium";v="{v}", "Google Chrome";v="{v}", "Not)A;Brand";v="24"`,
	`"Google Chrome";v="{v}", "Not)A;Brand";v="8", "Chromium";v="{v}"`,
	`"Not)A;Brand";v="99", "Google Chrome";v="{v}", "Chromium";v="{v}"`,
}

var digitsRe = regexp.MustCompile(`(\d+)`)

type FingerprintProfile struct {
	Impersonate string
	UAMajor     int
	UA          string
	SecChUA     string
	AcceptLang  string
	Screen      string
	Platform    string
	Key         string
	JitterLo    float64
	JitterHi    float64
}

var activeFP *FingerprintProfile

func fp() *FingerprintProfile {
	if activeFP == nil {
		p := randomFingerprint()
		activeFP = &p
	}
	return activeFP
}

func setFingerprint(profile FingerprintProfile) {
	activeFP = &profile
}

func randomFingerprint() FingerprintProfile {
	tls := tlsProfiles[rand.Intn(len(tlsProfiles))]
	tlsMaj := 146
	if m := digitsRe.FindStringSubmatch(tls); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &tlsMaj)
	}
	uaMaj := tlsMaj

	brand := strings.ReplaceAll(brandTemplates[rand.Intn(len(brandTemplates))], "{v}", fmt.Sprintf("%d", uaMaj))
	lang := acceptLangs[rand.Intn(len(acceptLangs))]
	screen := screens[rand.Intn(len(screens))]
	ua := fmt.Sprintf(
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
		uaMaj,
	)
	p := FingerprintProfile{
		Impersonate: tls,
		UAMajor:     uaMaj,
		UA:          ua,
		SecChUA:     brand,
		AcceptLang:  lang,
		Screen:      screen,
		Platform:    "Windows",
		JitterLo:    0.15,
		JitterHi:    0.45 + rand.Float64()*0.5,
	}
	p.Key = fmt.Sprintf("%s|%d|%s|%s|%s", p.Impersonate, p.UAMajor, p.SecChUA, p.AcceptLang, p.Screen)
	return p
}

func clientProfileFor(impersonate string) profiles.ClientProfile {
	switch strings.ToLower(impersonate) {
	case "chrome131":
		return profiles.Chrome_131
	case "chrome133", "chrome133a":
		return profiles.Chrome_133
	case "chrome144":
		return profiles.Chrome_144
	case "chrome146":
		return profiles.Chrome_146
	default:
		return profiles.Chrome_146
	}
}



const (
	baseURL       = "https://tickets.la28.org"
	defaultTarget = baseURL + "/?affiliate=" + shopAffiliate + "&forceLogin=true"
	navAccept     = "text/html,application/xhtml+xml,application/xml;q=0.9," +
		"image/avif,image/webp,image/apng,*/*;q=0.8," +
		"application/signed-exchange;v=b3;q=0.7"
)

func resolveQueueEntryURL(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return target
	}
	path := u.Path
	if path == "" || path == "/" {
		u.Path = "/mycustomerdata/"
		q := u.Query()
		if q.Get("forceLogin") == "" && strings.Contains(strings.ToLower(target), "forcelogin") {
			q.Set("forceLogin", "true")
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	return target
}

func applyQueueTarget(cfg *SoftblockConfig, userTarget string) {
	if cfg == nil || userTarget == "" {
		return
	}
	cfg.TargetURL = userTarget
}

var configuredProxyURL string
var baseProxyURL string

func setProxyURL(raw string) error {
	normalized, err := normalizeProxyURL(raw)
	if err != nil {
		return err
	}
	baseProxyURL = normalized
	configuredProxyURL = normalized
	return nil
}

func applyStickyProxy(sessionID string) {
	if baseProxyURL == "" {
		return
	}
	configuredProxyURL = stickyProxyURL(baseProxyURL, sessionID)
}

func stickyProxyURL(proxyURL, sessionID string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return proxyURL
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	if i := strings.Index(user, "-session-"); i > 0 {
		user = user[:i]
	}
	user = user + "-session-" + sessionID
	u.User = url.UserPassword(user, pass)
	return u.String()
}

func proxyLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func normalizeProxyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		if u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("invalid proxy URL")
		}
		return u.String(), nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) == 2 {
		return "http://" + parts[0] + ":" + parts[1], nil
	}
	if len(parts) >= 4 {
		host, port, user := parts[0], parts[1], parts[2]
		pass := strings.Join(parts[3:], ":")
		u := url.UserPassword(user, pass)
		return fmt.Sprintf("http://%s@%s:%s", u.String(), host, port), nil
	}
	return "", fmt.Errorf("proxy must be host:port:user:pass or a full URL")
}

func tlsClientOpts(jar tls_client.CookieJar, followRedirects bool) []tls_client.HttpClientOption {
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(45),
		tls_client.WithClientProfile(clientProfileFor(fp().Impersonate)),
		tls_client.WithCookieJar(jar),
	}
	if !followRedirects {
		opts = append(opts, tls_client.WithNotFollowRedirects())
	}
	if configuredProxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(configuredProxyURL))
	}
	return opts
}

type Session struct {
	Client tls_client.HttpClient
	jar    tls_client.CookieJar
}

func makeSession() (*Session, error) {
	return makeSessionOpts(true)
}

func makeSessionOpts(followRedirects bool) (*Session, error) {
	jar := tls_client.NewCookieJar()
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), tlsClientOpts(jar, followRedirects)...)
	if err != nil {
		return nil, err
	}
	return &Session{Client: client, jar: jar}, nil
}

func (s *Session) noRedirectClient() (tls_client.HttpClient, error) {
	return tls_client.NewHttpClient(
		tls_client.NewNoopLogger(),
		tlsClientOpts(s.jar, false)...,
	)
}

func publicIP(session *Session) (string, error) {
	return publicIPRetry(session, 3)
}

func publicIPRetry(session *Session, tries int) (string, error) {
	if tries <= 0 {
		tries = 1
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		resp, err := session.do("GET", "https://api.ipify.org", chromeHeaders(map[string]string{"accept": "*/*"}, "navigate"), nil, 20*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(i+1) * time.Second)
			continue
		}
		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("ipify HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(i+1) * time.Second)
			continue
		}
		return strings.TrimSpace(resp.Body), nil
	}
	return "", lastErr
}

func chromeHeaders(extra map[string]string, kind string) []headerKV {
	p := fp()
	h := make([]headerKV, 0, 20)
	h = append(h,
		headerKV{"sec-ch-ua", p.SecChUA},
		headerKV{"sec-ch-ua-mobile", "?0"},
		headerKV{"sec-ch-ua-platform", fmt.Sprintf(`"%s"`, p.Platform)},
	)

	switch kind {
	case "navigate":
		h = append(h,
			headerKV{"upgrade-insecure-requests", "1"},
			headerKV{"user-agent", p.UA},
			headerKV{"accept", navAccept},
			headerKV{"sec-fetch-site", "none"},
			headerKV{"sec-fetch-mode", "navigate"},
			headerKV{"sec-fetch-user", "?1"},
			headerKV{"sec-fetch-dest", "document"},
			headerKV{"accept-language", p.AcceptLang},
		)
	case "script":
		h = append(h,
			headerKV{"user-agent", p.UA},
			headerKV{"accept", "*/*"},
			headerKV{"sec-fetch-site", "same-origin"},
			headerKV{"sec-fetch-mode", "no-cors"},
			headerKV{"sec-fetch-dest", "script"},
			headerKV{"accept-language", p.AcceptLang},
		)
	case "post_sensor":
		h = append(h,
			headerKV{"user-agent", p.UA},
			headerKV{"accept", "*/*"},
			headerKV{"content-type", "text/plain;charset=UTF-8"},
			headerKV{"origin", baseURL},
			headerKV{"sec-fetch-site", "same-origin"},
			headerKV{"sec-fetch-mode", "cors"},
			headerKV{"sec-fetch-dest", "empty"},
			headerKV{"accept-language", p.AcceptLang},
		)
	default:
		h = append(h,
			headerKV{"user-agent", p.UA},
			headerKV{"accept", "application/json, text/javascript, */*; q=0.01"},
			headerKV{"content-type", "application/json"},
			headerKV{"origin", baseURL},
			headerKV{"sec-fetch-site", "same-origin"},
			headerKV{"sec-fetch-mode", "cors"},
			headerKV{"sec-fetch-dest", "empty"},
			headerKV{"accept-language", p.AcceptLang},
			headerKV{"x-requested-with", "XMLHttpRequest"},
		)
	}

	if extra != nil {
		byName := map[string]int{}
		for i, kv := range h {
			byName[strings.ToLower(kv.Key)] = i
		}
		for k, v := range extra {
			lk := strings.ToLower(k)
			if idx, ok := byName[lk]; ok {
				h[idx].Value = v
			} else {
				h = append(h, headerKV{k, v})
			}
		}
	}
	return h
}

type headerKV struct {
	Key, Value string
}

type HTTPResult struct {
	StatusCode int
	URL        string
	Body       string
	Header     http.Header
}

func (s *Session) do(method, rawURL string, headers []headerKV, body io.Reader, timeout time.Duration) (*HTTPResult, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(headers))
	for _, kv := range headers {
		req.Header.Set(kv.Key, kv.Value)
		order = append(order, strings.ToLower(kv.Key))
	}
	req.Header[http.HeaderOrderKey] = order

	if timeout > 0 {
		_ = timeout
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &HTTPResult{
		StatusCode: resp.StatusCode,
		URL:        finalURL,
		Body:       string(b),
		Header:     resp.Header,
	}, nil
}

func cookieValue(session *Session, name, defaultVal string) string {
	u, err := url.Parse(baseURL + "/")
	if err != nil {
		return defaultVal
	}
	for _, c := range session.Client.GetCookies(u) {
		if c.Name == name {
			if c.Value == "" {
				return defaultVal
			}
			return c.Value
		}
	}
	return defaultVal
}

func cookieNames(session *Session) []string {
	u, _ := url.Parse(baseURL + "/")
	seen := map[string]struct{}{}
	for _, c := range session.Client.GetCookies(u) {
		seen[c.Name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func allCookies(session *Session) map[string]string {
	return cookiesForURL(session, baseURL+"/")
}

func cookiesForURL(session *Session, raw string) map[string]string {
	u, err := url.Parse(raw)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, c := range session.Client.GetCookies(u) {
		out[c.Name] = c.Value
	}
	return out
}

func seedSiteCookies(session *Session) {
	u, _ := url.Parse(baseURL + "/")
	jar := []struct{ name, value string }{
		{"28A.webid", "MjhBX05PX1NFU1NJT05fRU4="},
		{
			"28A.webshop",
			"bG9naW49MCZhbW91bnQ9MCZyZXNlcnZhdGlvbl9kdXJhdGlvbj0wJmFtb3VudF9ldmlkcz0w" +
				"JmtleT1nTlhYNko1aXZHTXUmZGV2aWNlX2luZm89MCZsYW5ndWFnZT1lbiZsb2NhbGU9ZW5f" +
				"dXMmY3VycmVuY3lfY29kZT1VU0Q=",
		},
	}
	for _, item := range jar {
		if cookieValue(session, item.name, "") != "" {
			continue
		}
		session.Client.SetCookies(u, []*http.Cookie{{
			Name:   item.name,
			Value:  item.value,
			Domain: "tickets.la28.org",
			Path:   "/",
		}})
	}
	fmt.Printf("[prep] site cookies: %v\n", cookieNames(session))
}

func isAkamaiInterstitial(html string) bool {
	h := html
	return strings.Contains(h, "sec-if-cpt-container") ||
		strings.Contains(h, "scf-akamai-protected-by") ||
		(strings.Contains(h, "behavioral-content") && strings.Contains(strings.ToLower(h), "akamai"))
}

func quoteLikePython(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func encodeQuery(pairs [][2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, quoteLikePython(p[0])+"="+quoteLikePython(p[1]))
	}
	return strings.Join(parts, "&")
}

func headerGet(h http.Header, key string) string {
	if v := h.Get(key); v != "" {
		return v
	}
	return h.Get(strings.ToLower(key))
}



var defaultQPN = map[string]string{
	"SoftBlockCaptchaSession": "scv",
	"CultureId":               "cid",
	"LayoutName":              "l",
	"TargetUrl":               "t",
	"QueueItEnqueueToken":     "qet",
	"EnqueueToken":            "enqueuetoken",
}

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

const (
	la28QueueCustomer = "web"
	la28QueueEvent    = "la28tgigy260324"
)

func buildSoftblockURL(target string) string {
	t := orDefault(target, defaultTarget)
	return fmt.Sprintf(
		"%s/queue/softblock/?c=%s&e=%s&t=%s&rticr=2",
		baseURL, la28QueueCustomer, la28QueueEvent, quoteLikePython(t),
	)
}

func isHardBlockHTML(html string) bool {
	return strings.Contains(html, "Hard Block")
}

func isInqueueView(u, html string) bool {
	lu := strings.ToLower(u)
	return strings.Contains(lu, "/queue/view") ||
		strings.Contains(html, "InQueueViewModel") ||
		strings.Contains(html, `data-pageid="queue"`)
}

func parseInqueueConfig(html, pageURL, target string) (*SoftblockConfig, error) {
	opts := parseInqueueOptions(html)
	u, err := url.Parse(pageURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()

	customerID := optString(opts, "customerId")
	if customerID == "" {
		customerID = q.Get("c")
	}
	eventID := optString(opts, "eventId")
	if eventID == "" {
		eventID = q.Get("e")
	}
	if customerID == "" || eventID == "" {
		return nil, fmt.Errorf("inqueue config missing customer/event (url=%s)", truncate(pageURL, 120))
	}

	targetURL := q.Get("t")
	if targetURL != "" {
		if decoded, err := url.QueryUnescape(targetURL); err == nil {
			targetURL = decoded
		}
	}
	if targetURL == "" {
		targetURL = orDefault(target, defaultTarget)
	}

	layout := optString(opts, "layout")
	if layout == "" {
		layout, _ = softblockField(html, "layout")
	}
	if layout == "" {
		return nil, fmt.Errorf("inqueue config missing layout")
	}

	culture := "en-US"
	if c, err := softblockField(html, "culture"); err == nil && c != "" {
		culture = c
	}
	qpp := "/queue"
	if p, err := softblockField(html, "queuePathPrefix"); err == nil && p != "" {
		qpp = p
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

	hash := optString(opts, "challengeApiChecksumHash")
	if hash == "" {
		hash, _ = softblockField(html, "challengeApiChecksumHash")
	}

	return &SoftblockConfig{
		CustomerID:               customerID,
		EventID:                  eventID,
		TargetURL:                targetURL,
		Layout:                   layout,
		Culture:                  culture,
		QueuePathPrefix:          qpp,
		ChallengeAPIChecksumHash: hash,
		QueryParameterNames:      qpn,
	}, nil
}

func optString(opts map[string]any, key string) string {
	v, ok := opts[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
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
	if queueitEnqueueToken != "" {
		pairs = append(pairs, [2]string{qpn["QueueItEnqueueToken"], queueitEnqueueToken})
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



func twocaptchaPost(path string, payload map[string]any, retries int) (map[string]any, error) {
	if retries <= 0 {
		retries = 5
	}
	url := "https://api.2captcha.com/" + path
	p := fp()
	jar := tls_client.NewCookieJar()
	client, err := tls_client.NewHttpClient(
		tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(clientProfileFor(p.Impersonate)),
		tls_client.WithCookieJar(jar),
	)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(raw))
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", p.UA)
		req.Header[http.HeaderOrderKey] = []string{"content-type", "user-agent"}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("2captcha HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			preview := string(body)
			if len(preview) > 200 {
				preview = preview[:200]
			}
			return nil, fmt.Errorf("2captcha HTTP %d: %s", resp.StatusCode, preview)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		return out, nil
	}
	return nil, fmt.Errorf("2captcha %s failed after retries: %v", path, lastErr)
}

func twocaptchaImageText(imageB64, apiKey string) (string, error) {
	created, err := twocaptchaPost("createTask", map[string]any{
		"clientKey": apiKey,
		"task": map[string]any{
			"type":      "ImageToTextTask",
			"body":      imageB64,
			"phrase":    false,
			"case":      true,
			"numeric":   0,
			"math":      false,
			"minLength": 3,
			"maxLength": 8,
			"comment":   "Type the characters shown in the captcha image exactly.",
		},
	}, 5)
	if err != nil {
		return "", err
	}
	if eid, _ := created["errorId"].(float64); eid != 0 {
		return "", fmt.Errorf("2captcha createTask: %v", created)
	}
	taskID := created["taskId"]
	fmt.Printf("[2captcha] ImageToText taskId=%v\n", taskID)

	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		result, err := twocaptchaPost("getTaskResult", map[string]any{
			"clientKey": apiKey,
			"taskId":    taskID,
		}, 5)
		if err != nil {
			return "", err
		}
		if eid, _ := result["errorId"].(float64); eid != 0 {
			return "", fmt.Errorf("2captcha getTaskResult: %v", result)
		}
		if status, _ := result["status"].(string); status == "ready" {
			text := ""
			if sol, ok := result["solution"].(map[string]any); ok {
				text, _ = sol["text"].(string)
			}
			fmt.Printf("[2captcha] solution=%q\n", text)
			return strings.TrimSpace(text), nil
		}
	}
	return "", fmt.Errorf("2captcha ImageToText timeout")
}

func solveBotDetect(session *Session, cfg *SoftblockConfig, apiKey string, maxOCR int) (map[string]any, error) {
	if maxOCR <= 0 {
		maxOCR = 3
	}
	culture := strings.ToLower(cfg.Culture)
	if culture == "" {
		culture = "en-us"
	}
	challengeURL := fmt.Sprintf("%s%s/challengeapi/queueitcaptcha/challenge/%s", baseURL, cfg.QueuePathPrefix, culture)
	sessionID := ""
	var lastPayload any

	for ocrTry := 1; ocrTry <= maxOCR; ocrTry++ {
		fetchURL := challengeURL
		if sessionID != "" {
			fetchURL = challengeURL + "/" + sessionID
		}
		t0 := time.Now()

		hdrs := chromeHeaders(map[string]string{
			"accept":         "application/json, text/javascript, */*; q=0.01",
			"content-type":   "application/json",
			"origin":         baseURL,
			"referer":        baseURL + cfg.QueuePathPrefix + "/softblock/",
			"sec-fetch-dest": "empty",
			"sec-fetch-mode": "cors",
			"sec-fetch-site": "same-origin",
			"x-requested-with": "XMLHttpRequest",
		}, "xhr")
		for k, v := range challengeHeaders(cfg, true) {
			hdrs = append(hdrs, headerKV{k, v})
		}

		r, err := session.do("POST", fetchURL, hdrs, strings.NewReader(""), 0)
		if err != nil {
			return nil, err
		}
		fmt.Printf("[botdetect] challenge POST -> %d (ocr %d/%d)\n", r.StatusCode, ocrTry, maxOCR)
		if r.StatusCode != 200 {
			preview := r.Body
			if len(preview) > 300 {
				preview = preview[:300]
			}
			return nil, fmt.Errorf("challenge fetch failed: %d %s", r.StatusCode, preview)
		}
		var ch map[string]any
		if err := json.Unmarshal([]byte(r.Body), &ch); err != nil {
			return nil, err
		}
		if sid, ok := ch["sessionId"].(string); ok && sid != "" {
			sessionID = sid
		}
		details := ch["challengeDetails"]
		imageB64, _ := ch["imageBase64"].(string)
		if imageB64 == "" {
			keys := make([]string, 0, len(ch))
			for k := range ch {
				keys = append(keys, k)
			}
			return nil, fmt.Errorf("no imageBase64 in challenge: keys=%v", keys)
		}

		solution, err := twocaptchaImageText(imageB64, apiKey)
		if err != nil {
			return nil, err
		}
		p := fp()
		duration := int(time.Since(t0).Milliseconds())
		if duration < 1 {
			duration = 1
		}
		stats := map[string]any{
			"browser":        "Chrome",
			"browserVersion": fmt.Sprintf("%d.0.0.0", p.UAMajor),
			"cookiesEnabled": true,
			"duration":       duration,
			"isMobile":       false,
			"os":             "Windows",
			"osVersion":      "10",
			"screen":         p.Screen,
			"tries":          ocrTry,
			"userAgent":      p.UA,
		}

		verifyURL := baseURL + cfg.ChallengeVerifyEndpoint
		body := map[string]any{
			"challengeType":             "botdetect",
			"sessionId":                 sessionID,
			"challengeDetails":          details,
			"solution":                  solution,
			"stats":                     stats,
			"customerId":                cfg.CustomerID,
			"eventId":                   cfg.EventID,
			"challengesIssuedByReason":  cfg.ChallengesIssuedByReason,
			"version":                   6,
		}
		bodyJSON, _ := json.Marshal(body)

		vh := chromeHeaders(map[string]string{
			"accept":          "application/json, text/javascript, */*; q=0.01",
			"content-type":    "application/json",
			"origin":          baseURL,
			"referer":         baseURL + cfg.QueuePathPrefix + "/softblock/",
			"sec-fetch-dest":  "empty",
			"sec-fetch-mode":  "cors",
			"sec-fetch-site":  "same-origin",
			"x-requested-with": "XMLHttpRequest",
		}, "xhr")
		for k, v := range challengeHeaders(cfg, false) {
			vh = append(vh, headerKV{k, v})
		}

		vr, err := session.do("POST", verifyURL, vh, strings.NewReader(string(bodyJSON)), 0)
		if err != nil {
			return nil, err
		}
		preview := vr.Body
		if len(preview) > 240 {
			preview = preview[:240]
		}
		fmt.Printf("[botdetect] verify -> %d %s\n", vr.StatusCode, preview)
		if vr.StatusCode != 200 {
			p2 := vr.Body
			if len(p2) > 300 {
				p2 = p2[:300]
			}
			return nil, fmt.Errorf("BotDetect verify HTTP %d: %s", vr.StatusCode, p2)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(vr.Body), &data); err != nil {
			return nil, err
		}
		lastPayload = data
		if data["sessionInfo"] != nil || truthy(data["isVerified"]) || truthy(data["verified"]) {
			if si, ok := data["sessionInfo"].(map[string]any); ok {
				return si, nil
			}
			return data, nil
		}
		if _, ok := data["sessionId"]; ok {
			return data, nil
		}
		if _, ok := data["challengeSessionId"]; ok {
			return data, nil
		}
		errs, _ := data["errorCodes"].([]any)
		wrong := false
		for _, e := range errs {
			if s, ok := e.(string); ok && s == "WrongUserInput" {
				wrong = true
				break
			}
		}
		if wrong && ocrTry < maxOCR {
			fmt.Println("[botdetect] WrongUserInput â€” new image (same Softblock visit)")
			continue
		}
		break
	}
	return nil, fmt.Errorf("BotDetect verify failed: %v", lastPayload)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	default:
		return v != nil
	}
}



var (
	scriptSrcRe     = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+)["']`)
	sbsdChallengeRe = regexp.MustCompile(`(?i)["'](/[^"']+\?v=[0-9a-fA-F-]{36}(?:&|&amp;)t=\d+)["']`)
)

const maxSensorPosts = 3

func akamaiScriptURLs(html, pageURL string) []string {
	out := make([]string, 0, 4)

	if path, err := akamai.ParseScriptPath(strings.NewReader(html)); err == nil && path != "" {
		out = append(out, mustJoinURL(pageURL, path))
	}

	for _, src := range scriptSrcRe.FindAllStringSubmatch(html, -1) {
		if len(src) < 2 {
			continue
		}
		s := src[1]
		if strings.Contains(s, "akamai.com") || strings.Contains(s, "/akam/") || strings.Contains(s, "pixel_") {
			continue
		}
		s = strings.ReplaceAll(s, "&amp;", "&")
		if strings.Contains(s, "t=") {
			continue
		}
		if strings.HasPrefix(s, "//") {
			s = "https:" + s
		} else if strings.HasPrefix(s, "/") {
			s = mustJoinURL(pageURL, s)
		}
		if !containsStr(out, s) {
			out = append(out, s)
		}
	}

	host := ""
	if u, err := url.Parse(pageURL); err == nil {
		host = u.Host
	}
	same := make([]string, 0, len(out))
	for _, u := range out {
		pu, err := url.Parse(u)
		if err != nil {
			continue
		}
		if pu.Host == "" || pu.Host == host {
			same = append(same, u)
		}
	}
	if len(same) > 0 {
		return same
	}
	return out
}

func mustJoinURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func fpSleep() {
	p := fp()
	lo, hi := p.JitterLo, p.JitterHi
	if hi <= lo {
		lo, hi = 0.15, 0.7
	}
	d := lo + rand.Float64()*(hi-lo)
	time.Sleep(time.Duration(d * float64(time.Second)))
}

func isQueueURL(u string) bool {
	lu := strings.ToLower(u)
	return strings.Contains(lu, "softblock") ||
		strings.Contains(lu, "/queue/") ||
		strings.Contains(lu, "queueittoken")
}

func isSBSDChallengePage(html string) bool {
	return sbsdChallengeRe.FindStringSubmatch(html) != nil &&
		strings.Contains(html, "sec-if-cpt-container")
}

func findSensorURL(html, pageURL string) (string, error) {
	urls := akamaiScriptURLs(html, pageURL)
	for _, u := range urls {
		if !strings.Contains(u, "t=") {
			return u, nil
		}
	}
	if path, err := akamai.ParseScriptPath(strings.NewReader(html)); err == nil && path != "" {
		return mustJoinURL(pageURL, path), nil
	}
	paths := regexp.MustCompile(`(?i)<script[^>]+src=["'](/[^"'?]+)["']`).FindAllStringSubmatch(html, -1)
	var last string
	for _, m := range paths {
		if len(m) < 2 {
			continue
		}
		p := m[1]
		if strings.Contains(p, "twam0") {
			continue
		}
		last = p
	}
	if last != "" {
		return mustJoinURL(pageURL, last), nil
	}
	return "", fmt.Errorf("Akamai sensor script path not found")
}

type sensorState struct {
	URL     string
	Script  string
	Context string
	Posted  int
}

func solveSensors(
	session *Session,
	hyperSession *hyper.Session,
	html, pageURL, ip string,
	rounds int,
	state *sensorState,
) (int, error) {
	if rounds <= 0 {
		rounds = 1
	}
	if state == nil {
		state = &sensorState{}
	}
	if state.URL == "" {
		u, err := findSensorURL(html, pageURL)
		if err != nil {
			return 0, err
		}
		state.URL = u
	}
	if state.Script == "" {
		sr, err := session.do("GET", state.URL, chromeHeaders(map[string]string{"referer": pageURL}, "script"), nil, 0)
		if err != nil {
			return 0, err
		}
		tail := state.URL
		if len(tail) > 60 {
			tail = tail[len(tail)-60:]
		}
		fmt.Printf("[akamai] script ...%s -> %d len=%d\n", tail, sr.StatusCode, len(sr.Body))
		state.Script = sr.Body
	}

	posted := 0
	p := fp()
	for posted < rounds {
		abck := cookieValue(session, "_abck", "")
		bmsz := cookieValue(session, "bm_sz", "")
		if abck == "" || bmsz == "" {
			return posted, fmt.Errorf("missing _abck/bm_sz before sensor")
		}
		needScript := posted == 0 && state.Context == ""
		script := ""
		if needScript {
			script = state.Script
		}
		sensor, ctx, err := hyperSession.GenerateSensorData(context.Background(), &hyper.SensorInput{
			Abck:           abck,
			Bmsz:           bmsz,
			Version:        "3",
			PageUrl:        pageURL,
			UserAgent:      p.UA,
			IP:             ip,
			AcceptLanguage: p.AcceptLang,
			Context:        state.Context,
			Script:         script,
			ScriptUrl:      state.URL,
		})
		if err != nil {
			return posted, err
		}
		state.Context = ctx

		payload, _ := json.Marshal(map[string]string{"sensor_data": sensor})
		pr, err := session.do("POST", state.URL, chromeHeaders(map[string]string{
			"referer":      pageURL,
			"content-type": "application/json",
			"accept":       "*/*",
		}, "xhr"), strings.NewReader(string(payload)), 0)
		if err != nil {
			return posted, err
		}
		posted++
		state.Posted++
		abck = cookieValue(session, "_abck", "")
		valid := strings.Contains(abck, "~0~") || akamai.IsCookieValid(abck, posted)
		fmt.Printf("[akamai] sensor[%d] post=%d abck len=%d has_~0~=%v valid=%v\n",
			state.Posted, pr.StatusCode, len(abck), strings.Contains(abck, "~0~"), valid)
		if valid {
			break
		}
		fpSleep()
	}
	return posted, nil
}

func solveSBSDChallenge(session *Session, hyperSession *hyper.Session, html, pageURL, ip string) (int, error) {
	m := sbsdChallengeRe.FindStringSubmatch(html)
	if m == nil {
		return 0, fmt.Errorf("SBSD challenge script URL not found")
	}
	scriptSrc := strings.ReplaceAll(m[1], "&amp;", "&")
	scriptURL := mustJoinURL(pageURL, scriptSrc)
	u, err := url.Parse(scriptURL)
	if err != nil {
		return 0, err
	}
	q := u.Query()
	uuidV := q.Get("v")
	t := q.Get("t")
	if uuidV == "" {
		return 0, fmt.Errorf("SBSD uuid missing: %s", scriptURL)
	}

	sr, err := session.do("GET", scriptURL, chromeHeaders(map[string]string{"referer": pageURL}, "script"), nil, 0)
	if err != nil {
		return 0, err
	}
	oCookie := cookieValue(session, "sbsd_o", "")
	if oCookie == "" {
		oCookie = cookieValue(session, "bm_so", "")
	}
	p := fp()
	body, err := hyperSession.GenerateSbsdData(context.Background(), &hyper.SbsdInput{
		Index:          0,
		UserAgent:      p.UA,
		Uuid:           uuidV,
		PageUrl:        pageURL,
		OCookie:        oCookie,
		Script:         sr.Body,
		AcceptLanguage: p.AcceptLang,
		IP:             ip,
	})
	if err != nil {
		return 0, err
	}
	postURL := mustJoinURL(pageURL, u.Path)
	if t != "" {
		postURL += "?t=" + t
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	pr, err := session.do("POST", postURL, chromeHeaders(map[string]string{
		"referer":      pageURL,
		"content-type": "application/json",
		"accept":       "*/*",
	}, "xhr"), strings.NewReader(string(payload)), 0)
	if err != nil {
		return 0, err
	}
	fmt.Printf("[sbsd] challenge post=%d\n", pr.StatusCode)
	return pr.StatusCode, nil
}

func tryPixel(session *Session, hyperSession *hyper.Session, html, pageURL, ip string) bool {
	htmlVar, err := akamai.ParsePixelHtmlVar(strings.NewReader(html))
	if err != nil {
		return false
	}
	scriptURL, postURL, err := akamai.ParsePixelScriptURL(strings.NewReader(html))
	if err != nil {
		return false
	}
	if strings.HasPrefix(scriptURL, "/") {
		scriptURL = mustJoinURL(pageURL, scriptURL)
	}
	if strings.HasPrefix(postURL, "/") {
		postURL = mustJoinURL(pageURL, postURL)
	}
	sr, err := session.do("GET", scriptURL, chromeHeaders(map[string]string{"referer": pageURL}, "script"), nil, 0)
	if err != nil {
		return false
	}
	scriptVar, err := akamai.ParsePixelScriptVar(strings.NewReader(sr.Body))
	if err != nil {
		return false
	}
	p := fp()
	payload, err := hyperSession.GeneratePixelData(context.Background(), &hyper.PixelInput{
		UserAgent:      p.UA,
		HTMLVar:        fmt.Sprintf("%d", htmlVar),
		ScriptVar:      scriptVar,
		AcceptLanguage: p.AcceptLang,
		IP:             ip,
	})
	if err != nil {
		return false
	}
	hdrs := chromeHeaders(map[string]string{
		"referer":      pageURL,
		"content-type": "application/x-www-form-urlencoded",
		"accept":       "*/*",
	}, "xhr")
	filtered := make([]headerKV, 0, len(hdrs))
	for _, kv := range hdrs {
		if strings.EqualFold(kv.Key, "x-requested-with") {
			continue
		}
		filtered = append(filtered, kv)
	}
	pr, err := session.do("POST", postURL, filtered, strings.NewReader(payload), 0)
	if err != nil {
		return false
	}
	ok := pr.StatusCode >= 200 && pr.StatusCode < 300
	fmt.Printf("[pixel] post=%d ok=%v\n", pr.StatusCode, ok)
	return ok
}

func extractQueueLink(html, pageURL string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)(https://tickets\.la28\.org/queue/[^\s"'<>]+)`),
		regexp.MustCompile(`(?i)(/queue/[^\s"'<>]+)`),
	}
	for _, re := range patterns {
		if m := re.FindStringSubmatch(html); len(m) > 1 {
			link := m[1]
			if strings.HasPrefix(link, "/") {
				link = mustJoinURL(pageURL, link)
			}
			if isQueueURL(link) || strings.Contains(link, "softblock") {
				return link
			}
		}
	}
	return ""
}

func followToQueue(session *Session, startURL string) (finalURL, html string, err error) {
	u := startURL
	if u == "" {
		u = defaultTarget
	}
	entryURL := u
	for step := 0; step < 8; step++ {
		extra := map[string]string{}
		if step > 0 {
			extra["referer"] = baseURL + "/"
			extra["sec-fetch-site"] = "same-origin"
		}
		r, err := session.do("GET", u, chromeHeaders(extra, "navigate"), nil, 0)
		if err != nil {
			return "", "", err
		}
		html = r.Body
		finalURL = r.URL
		if isSBSDChallengePage(html) {
			return finalURL, html, nil
		}
		if isQueueURL(finalURL) || strings.Contains(html, "SoftblockViewModel") || strings.Contains(html, "ruleName") {
			return finalURL, html, nil
		}
		if link := extractQueueLink(html, finalURL); link != "" {
			u = link
			continue
		}
		if step == 0 && strings.Contains(finalURL, "tickets.la28.org") && !isQueueURL(finalURL) {
			u = entryURL
			continue
		}
		if step == 1 && u == entryURL && entryURL != defaultTarget {
			u = defaultTarget
			continue
		}
		break
	}
	return finalURL, html, nil
}

func enrichAndEnterQueue(session *Session, hyperSession *hyper.Session, ip, target string) (finalURL, html string, err error) {
	home := baseURL + "/"
	fmt.Printf("[enrich] home=%s\n", home)

	r0, err := session.do("GET", home, chromeHeaders(nil, "navigate"), nil, 0)
	if err != nil {
		return "", "", err
	}
	html = r0.Body
	pageURL := r0.URL
	fmt.Printf("[enrich] GET home -> %d len=%d\n", r0.StatusCode, len(html))

	prefix := html
	if len(prefix) > 500 {
		prefix = prefix[:500]
	}
	if r0.StatusCode == 403 || strings.Contains(prefix, "Access Denied") {
		return "", "", fmt.Errorf("Akamai hard block on first GET (TLS/IP/fingerprint)")
	}

	if isSBSDChallengePage(html) {
		fmt.Println("[sbsd] challenge page")
		if _, err := solveSBSDChallenge(session, hyperSession, html, pageURL, ip); err != nil {
			return "", "", err
		}
		fpSleep()
		r1, err := session.do("GET", home, chromeHeaders(nil, "navigate"), nil, 0)
		if err != nil {
			return "", "", err
		}
		html, pageURL = r1.Body, r1.URL
		if isSBSDChallengePage(html) {
			if _, err := solveSBSDChallenge(session, hyperSession, html, pageURL, ip); err != nil {
				return "", "", err
			}
			r1, err = session.do("GET", home, chromeHeaders(nil, "navigate"), nil, 0)
			if err != nil {
				return "", "", err
			}
			html, pageURL = r1.Body, r1.URL
		}
	}

	state := &sensorState{}
	sensorsPosted, err := solveSensors(session, hyperSession, html, pageURL, ip, maxSensorPosts, state)
	if err != nil {
		return "", "", err
	}

	pixelOK := tryPixel(session, hyperSession, html, pageURL, ip)
	fpSleep()

	trigger := target
	if trigger == "" {
		trigger = defaultTarget
	}
	entryURL := resolveQueueEntryURL(trigger)
	if entryURL != trigger {
		fmt.Printf("[enrich] queue entry resolved %s -> %s\n", trigger, entryURL)
	}

	softFirst := buildSoftblockURL(trigger)
	sr, serr := session.do("GET", softFirst, chromeHeaders(map[string]string{
		"referer":        baseURL + "/",
		"sec-fetch-site": "same-origin",
	}, "navigate"), nil, 0)
	if serr == nil && isSoftblockPage(sr.URL, sr.Body) && !isHardBlockHTML(sr.Body) {
		fmt.Printf("[enrich] softblock-first %s\n", truncate(sr.URL, 140))
		return sr.URL, sr.Body, nil
	}

	fmt.Printf("[enrich] queue trigger %s (sensors=%d pixel=%v)\n", entryURL, sensorsPosted, pixelOK)

	finalURL, qhtml, err := followToQueue(session, entryURL)
	if err != nil {
		return "", "", err
	}
	if isSBSDChallengePage(qhtml) {
		if _, err := solveSBSDChallenge(session, hyperSession, qhtml, finalURL, ip); err != nil {
			return "", "", err
		}
		finalURL, qhtml, err = followToQueue(session, entryURL)
		if err != nil {
			return "", "", err
		}
	}

	fmt.Printf("[enrich] entry %s sensors=%d\n", finalURL, sensorsPosted)
	return finalURL, qhtml, nil
}

func abckValid(session *Session) bool {
	abck := cookieValue(session, "_abck", "")
	return strings.Contains(abck, "~0~") || akamai.IsCookieValid(abck, maxSensorPosts)
}

func postHyperSensorOnce(session *Session, hyperSession *hyper.Session, pageURL, ip string, html *string) (string, error) {
	var body string
	if html == nil {
		r, err := session.do("GET", pageURL, chromeHeaders(nil, "navigate"), nil, 0)
		if err != nil {
			return "", err
		}
		fmt.Printf("[akamai] GET %s -> %d len=%d\n", pageURL, r.StatusCode, len(r.Body))
		prefix := r.Body
		if len(prefix) > 500 {
			prefix = prefix[:500]
		}
		if r.StatusCode == 403 || strings.Contains(prefix, "Access Denied") {
			return "", fmt.Errorf("Akamai hard block on first GET (TLS/IP/fingerprint)")
		}
		if isSBSDChallengePage(r.Body) {
			if _, err := solveSBSDChallenge(session, hyperSession, r.Body, r.URL, ip); err != nil {
				return "", err
			}
			r, err = session.do("GET", pageURL, chromeHeaders(nil, "navigate"), nil, 0)
			if err != nil {
				return "", err
			}
		}
		body = r.Body
		pageURL = r.URL
	} else {
		body = *html
	}

	if abckValid(session) {
		fmt.Println("[akamai] _abck already valid â€” skip sensor")
		return body, nil
	}

	state := &sensorState{}
	if _, err := solveSensors(session, hyperSession, body, pageURL, ip, 1, state); err != nil {
		fmt.Printf("[akamai] Hyper sensor skipped: %v\n", err)
		return body, nil
	}
	return body, nil
}



func saveQueueSession(session *Session, landingURL, queueID string, status map[string]any) error {
	fmt.Printf("[done] queue passed url=%s queueId=%s\n", truncate(landingURL, 120), queueID)
	return nil
}

func finishInqueueFlow(
	session *Session,
	hyperSession *hyper.Session,
	ip, target string,
	cfg *SoftblockConfig,
	inqueueHTML, viewRef, softblockRef, enqueueToken, scv string,
) (int, error) {
	inqOpts := parseInqueueOptions(inqueueHTML)
	if layout, ok := inqOpts["layout"].(string); ok && layout != "" {
		cfg.Layout = layout
	}
	if hash, ok := inqOpts["challengeApiChecksumHash"].(string); ok && hash != "" {
		cfg.ChallengeAPIChecksumHash = hash
	}
	bmReason := 1
	if r, ok := inqOpts["challengesIssuedByReason"].(int); ok {
		bmReason = r
	} else if r, ok := inqOpts["challengesIssuedByReason"].(float64); ok {
		bmReason = int(r)
	}
	var challengeNamesList []string
	if chs, ok := inqOpts["challenges"].([]map[string]any); ok {
		challengeNamesList = challengeNames(chs)
	} else if chs, ok := inqOpts["challenges"].([]any); ok {
		for _, c := range chs {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					challengeNamesList = append(challengeNamesList, n)
				}
			}
		}
	}
	fmt.Printf("[inqueue] challenges=%v showAkamai=%v ttl_ms=%v\n",
		challengeNamesList, inqOpts["showAkamaiBotManagerHeaderVerificationChallenge"],
		ttlString(enqueueTokenTTLMs(enqueueToken)))

	fmt.Println("[akamai] Hyper on /queue/view before BM/enqueue")
	htmlPtr := &inqueueHTML
	_, _ = postHyperSensorOnce(session, hyperSession, viewRef, ip, htmlPtr)

	var queueitEnqueueToken string
	needsBM := containsStr(challengeNamesList, "AkamaiBotManagerHeaderVerification")
	if show, ok := inqOpts["showAkamaiBotManagerHeaderVerificationChallenge"].(bool); ok && show {
		needsBM = true
	}
	if needsBM {
		var err error
		queueitEnqueueToken, err = solveAkamaiBMHeader(session, cfg, bmReason, viewRef)
		if err != nil {
			return 1, err
		}
	}
	fmt.Printf("[token] pre-enqueue ttl_ms=%v\n", ttlString(enqueueTokenTTLMs(enqueueToken)))

	applyQueueTarget(cfg, target)

	enq, err := enqueue(session, cfg, enqueueToken, scv, queueitEnqueueToken, viewRef, softblockRef, []any{})
	if err != nil {
		return 1, err
	}

	if restricted, _ := enq["restricted"].(bool); restricted {
		fmt.Printf("[enqueue] Restricted (Access Rule) (softblock_rule=%q)\n", cfg.RuleName)
		return 4, nil
	}

	queueID, _ := enq["queueId"].(string)
	if challengeRequired, _ := enq["challengeRequired"].(bool); challengeRequired {
		if redir, ok := enq["redirectUrl"].(string); ok {
			fmt.Println("[enqueue] challengeRequired ->", redir)
		}
		return 3, nil
	}
	if redir, ok := enq["redirectUrl"].(string); ok && redir != "" && queueID == "" {
		fmt.Println("[enqueue] redirect", redir)
		rr, err := session.do("GET", redir, chromeHeaders(map[string]string{
			"referer": baseURL + cfg.QueuePathPrefix + "/",
		}, "navigate"), nil, 0)
		if err != nil {
			return 1, err
		}
		fmt.Printf("[done] %d %s\n", rr.StatusCode, rr.URL)
		if err := saveQueueSession(session, rr.URL, "", nil); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if queueID == "" || strings.HasPrefix(queueID, "00000000") {
		return 1, fmt.Errorf("no queueId after enqueue: %v", enq)
	}

	fmt.Printf("[queue] queueId=%s\n", queueID)
	result, err := pollStatus(session, cfg, queueID, enqueueToken, 2.0, 3600.0)
	if err != nil {
		return 1, err
	}
	landingURL := target
	if redir, ok := result["redirectUrl"].(string); ok && redir != "" {
		rr, err := session.do("GET", redir, chromeHeaders(map[string]string{
			"referer": baseURL + cfg.QueuePathPrefix + "/",
		}, "navigate"), nil, 0)
		if err != nil {
			return 1, err
		}
		fmt.Printf("[done] %d %s\n", rr.StatusCode, rr.URL)
		landingURL = rr.URL
	}
	if err := saveQueueSession(session, landingURL, queueID, result); err != nil {
		return 1, err
	}
	return 0, nil
}

func clearSoftblockForSCV(
	session *Session,
	cfg *SoftblockConfig,
	hyperSession *hyper.Session,
	ip, twocaptchaKey, target, softblockRef, softblockHTML string,
) (*SoftblockConfig, string, string, string, string, string, error) {
	if strings.Contains(cfg.RuleName, "Hard Block") {
		return nil, "", "", "", "", "", fmt.Errorf("softblock hard block rule=%q", cfg.RuleName)
	}
	cfg, sessionInfo, inqueueHTML, viewRef, enqueueToken, softblockRef, err := clearSoftblockOnce(
		session, cfg, hyperSession, ip, twocaptchaKey, target, softblockRef, softblockHTML,
	)
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	var scv string
	if sessionInfo != nil {
		b, _ := json.Marshal(sessionInfo)
		scv = string(b)
	}
	return cfg, inqueueHTML, viewRef, softblockRef, enqueueToken, scv, nil
}

func ipForQueue(session *Session, enqueueToken, fallback string) string {
	if tip := enqueueTokenIP(enqueueToken); tip != nil {
		s := fmt.Sprint(tip)
		if s != "" && s != "<nil>" {
			if fallback != "" && s != fallback {
				fmt.Printf("[net] queue ip=%s (ipify=%s)\n", s, fallback)
			}
			return s
		}
	}
	return fallback
}

func runOnce(target, twocaptchaKey string, hyperSession *hyper.Session) (*Session, string, int, error) {
	applyStickyProxy(fmt.Sprintf("%d", time.Now().UnixNano()))
	profile := randomFingerprint()
	setFingerprint(profile)
	session, err := makeSession()
	if err != nil {
		return nil, "", 1, err
	}
	ip, err := publicIP(session)
	if err != nil {
		return nil, "", 1, err
	}
	fmt.Printf("[net] ip=%s impersonate=%s ua=Chrome/%d lang=%q screen=%q\n",
		ip, profile.Impersonate, profile.UAMajor, profile.AcceptLang, profile.Screen)
	fmt.Printf("[fp] sec-ch-ua=%s\n", profile.SecChUA)
	keyPrev := profile.Key
	if len(keyPrev) > 120 {
		keyPrev = keyPrev[:120]
	}
	fmt.Printf("[fp] key=%s...\n", keyPrev)

	finalURL, html, err := enrichAndEnterQueue(session, hyperSession, ip, target)
	if err != nil {
		return nil, "", 1, err
	}
	fmt.Printf("[entry] %s\n", finalURL)

	if !isSoftblockPage(finalURL, html) {
		if strings.Contains(finalURL, "queueittoken") {
			fmt.Println("[entry] queueittoken on direct path — done")
			if err := saveQueueSession(session, finalURL, "", nil); err != nil {
				return nil, "", 1, err
			}
			return session, ip, 0, nil
		}
		if isInqueueView(finalURL, html) {
			fmt.Println("[entry] reached queue/view without softblock â€” try enqueue")
			inqueueCfg, err := parseInqueueConfig(html, finalURL, target)
			if err != nil {
				return nil, "", 1, err
			}
			softblockRef := fmt.Sprintf(
				"%s%s/softblock/?c=%s&e=%s&t=%s&rticr=2",
				baseURL, inqueueCfg.QueuePathPrefix,
				quoteLikePython(inqueueCfg.CustomerID),
				quoteLikePython(inqueueCfg.EventID),
				quoteLikePython(target),
			)
			enqueueToken := extractEnqueueToken(finalURL)
			ip = ipForQueue(session, enqueueToken, ip)
			code, err := finishInqueueFlow(session, hyperSession, ip, target,
				inqueueCfg, html, finalURL, softblockRef, enqueueToken, "")
			if err != nil {
				return session, ip, code, err
			}
			if code == 0 {
				return session, ip, 0, nil
			}
			if code != 4 {
				return session, ip, code, nil
			}

			fmt.Println("[entry] enqueue needs scv â€” softblock captcha")
			sr, err := session.do("GET", softblockRef, chromeHeaders(map[string]string{
				"referer":        finalURL,
				"sec-fetch-site": "same-origin",
			}, "navigate"), nil, 0)
			if err != nil {
				return nil, "", 1, err
			}
			if !isSoftblockPage(sr.URL, sr.Body) {
				return session, ip, 4, fmt.Errorf("enqueue restricted and no softblock page (got %s)", truncate(sr.URL, 160))
			}
			cfg, err := parseSoftblockConfig(sr.Body)
			if err != nil {
				return nil, "", 1, err
			}
			names := challengeNames(cfg.Challenges)
			fmt.Printf("[softblock] c=%s e=%s challenges=%v rule=%q reason=%d\n",
				cfg.CustomerID, cfg.EventID, names, cfg.RuleName, cfg.ChallengesIssuedByReason)
			cfg, inqueueHTML, viewRef, softblockRef, enqueueToken, scv, err := clearSoftblockForSCV(
				session, cfg, hyperSession, ip, twocaptchaKey, target, sr.URL, sr.Body,
			)
			if err != nil {
				fmt.Printf("[fatal] Softblock clear failed: %v\n", err)
				if strings.Contains(err.Error(), "hard block") {
					return session, ip, 4, err
				}
				return session, ip, 3, nil
			}
			code, err = finishInqueueFlow(session, hyperSession, ip, target,
				cfg, inqueueHTML, viewRef, softblockRef, enqueueToken, scv)
			if err != nil {
				return session, ip, code, err
			}
			if code == 4 {
				return session, ip, 4, fmt.Errorf("enqueue restricted after softblock rule=%q", cfg.RuleName)
			}
			return session, ip, code, nil
		}
		fmt.Println("[entry] no softblock on direct path")
		return nil, "", 1, fmt.Errorf("unexpected entry page: %s", truncate(finalURL, 160))
	}

	cfg, err := parseSoftblockConfig(html)
	if err != nil {
		return nil, "", 1, err
	}
	names := challengeNames(cfg.Challenges)
	tok := extractEnqueueToken(cfg.inqueueURL())
	if tok == "" {
		tok = extractEnqueueToken(finalURL)
	}
	fmt.Printf("[softblock] c=%s e=%s challenges=%v rule=%q reason=%d token_ip=%v\n",
		cfg.CustomerID, cfg.EventID, names, cfg.RuleName, cfg.ChallengesIssuedByReason, enqueueTokenIP(tok))

	if strings.Contains(cfg.RuleName, "Hard Block") {
		return session, ip, 4, fmt.Errorf("softblock hard block rule=%q", cfg.RuleName)
	}
	if strings.Contains(cfg.RuleName, "Bot Deflector") {
		fmt.Println("[fp] Softblock Bot Deflector â€” proceed")
	}

	softblockRef := finalURL
	if !strings.Contains(finalURL, "softblock") {
		softblockRef = fmt.Sprintf(
			"%s%s/softblock/?c=%s&e=%s&t=%s&rticr=2",
			baseURL, cfg.QueuePathPrefix,
			quoteLikePython(cfg.CustomerID),
			quoteLikePython(cfg.EventID),
			quoteLikePython(orDefault(cfg.TargetURL, target)),
		)
	}

	cfg, sessionInfo, inqueueHTML, viewRef, enqueueToken, softblockRef, err := clearSoftblockOnce(
		session, cfg, hyperSession, ip, twocaptchaKey, target, softblockRef, html,
	)
	if err != nil {
		fmt.Printf("[fatal] Softblock clear failed: %v\n", err)
		return session, ip, 3, nil
	}

	var scv string
	if sessionInfo != nil {
		b, _ := json.Marshal(sessionInfo)
		scv = string(b)
	}
	code, err := finishInqueueFlow(session, hyperSession, ip, target,
		cfg, inqueueHTML, viewRef, softblockRef, enqueueToken, scv)
	return session, ip, code, err
}

type runOptions struct {
	target     string
	skipLogin  bool
	doBasket   bool
	filtered   []filteredTicket
	maxQty     int
	combo      Combo
}

func runCombo(opts runOptions) (*Session, *Client, *postQueueResult, int, error) {
	twocaptchaKey, err := requireEnv("TWOCAPTCHA_API_KEY")
	if err != nil {
		return nil, nil, nil, 1, err
	}
	hyperKey, err := requireEnv("HYPER_API_KEY")
	if err != nil {
		return nil, nil, nil, 1, err
	}
	hyperSession := hyper.NewSession(hyperKey)

	const maxAttempts = 8
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		session, ip, code, err := runOnce(opts.target, twocaptchaKey, hyperSession)
		if err == nil && code == 0 {
			fmt.Println("\nSuccess: passed queue")
			if opts.skipLogin {
				return session, nil, nil, 0, nil
			}
			pqCfg := postQueueConfig{
				doBasket: opts.doBasket,
				filtered: opts.filtered,
				maxQty:   opts.maxQty,
			}
			client, result, pqErr := runPostQueue(session, hyperSession, hyperKey, opts.target, ip,
				opts.combo.Email, opts.combo.Password, pqCfg)
			if pqErr == nil {
				return session, client, result, 0, nil
			}
			lastErr = pqErr
			if !isBurnedSessionErr(pqErr) || attempt == maxAttempts {
				return session, nil, nil, 1, pqErr
			}
			fmt.Printf("[restart] post-queue burned (%s) — new fingerprint (%d/%d)\n",
				truncate(pqErr.Error(), 140), attempt, maxAttempts)
			if client != nil {
				client.Close()
			}
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}
		if err == nil {
			return session, nil, nil, code, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return nil, nil, nil, code, err
		}
		if attempt < maxAttempts {
			fmt.Printf("[retry] %v — new fingerprint (%d/%d)\n", err, attempt, maxAttempts)
			time.Sleep(2 * time.Second)
		}
	}
	return nil, nil, nil, 1, lastErr
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "akamai hard block") ||
		strings.Contains(s, "softblock hard block") ||
		strings.Contains(s, "enqueue restricted") ||
		strings.Contains(s, "504") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "connection reset")
}



func requireEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("Missing %s in .env (see .env.example)", name)
	}
	return value, nil
}

func main() {
	_ = godotenv.Load()

	target := flag.String("target", defaultTarget, "Protected page URL")
	proxy := flag.String("proxy", os.Getenv("PROXY_URL"), "HTTP proxy (host:port:user:pass or http://user:pass@host:port)")
	combosPath := flag.String("combos", "combos.txt", "Path to email:pass combo file")
	skipLogin := flag.Bool("skip-login", false, "Only handle queue, do not auto-login")
	price := flag.Float64("price", -1, "Max ticket price for filter (prompted if omitted)")
	conditions := flag.String("conditions", "filter_conditions.txt", "Static event/category filter file")
	ticketsOut := flag.String("tickets-out", "filtered_tickets.json", "Write/read filtered tickets here")
	skipFilter := flag.Bool("skip-filter", false, "Skip re-fetch; load tickets from --tickets-out (must exist on disk)")
	maxQty := flag.Int("max-qty", defaultMaxEvenQty, "Max tickets to buy (2, 4, 6, 8, 10 or 12; never odd)")
	skipBasket := flag.Bool("skip-basket", false, "Skip add-to-basket after login")
	openBrowser := flag.Bool("open-browser", false, "After ATC, open Chrome with bot cookies on the cart page")
	flag.Parse()

	if strings.TrimSpace(*proxy) != "" {
		if err := setProxyURL(*proxy); err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: invalid proxy: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[net] proxy=%s\n", proxyLabel(configuredProxyURL))
	}

	var filtered []filteredTicket
	if !*skipFilter && !*skipBasket && !*skipLogin {
		fmt.Println("======== step 1: filter tickets ========")
		list, err := runTicketFilter(*price, *conditions, trackerURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error filtering tickets: %v\n", err)
			os.Exit(1)
		}
		filtered = list
		fmt.Println()
	} else if !*skipBasket && !*skipLogin {
		list, err := loadFilteredTickets(*ticketsOut)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", *ticketsOut, err)
			os.Exit(1)
		}
		filtered = list
		fmt.Printf("[filter] skip-filter: loaded %d ticket(s) from %s\n", len(filtered), *ticketsOut)
	}

	doBasket := !*skipBasket && !*skipLogin
	if doBasket {
		if len(filtered) == 0 {
			fmt.Fprintln(os.Stderr, "Error: ATC requires filtered tickets — none matched. Refusing freeform event ATC.")
			os.Exit(1)
		}
		if len(atcTargetsFromFiltered(filtered)) == 0 {
			fmt.Fprintln(os.Stderr, "Error: filtered tickets have no usable categories for ATC")
			os.Exit(1)
		}
		want := clampEvenQty(*maxQty)
		if want < minEvenQty {
			want = defaultMaxEvenQty
			fmt.Printf("[basket] --max-qty=%d is not a valid size (allowed: 2,4,6,8,10,12); using %d\n", *maxQty, want)
		} else if want != *maxQty {
			fmt.Printf("[basket] --max-qty=%d adjusted to %d (allowed: 2,4,6,8,10,12)\n", *maxQty, want)
		}
		*maxQty = want
		fmt.Printf("[basket] will ATC %d filtered target(s), max even qty=%d\n",
			len(atcTargetsFromFiltered(filtered)), *maxQty)
	}

	fmt.Println("======== step 2: queue + login ========")

	var combos []Combo
	if !*skipLogin {
		var err error
		combos, err = loadPostQueueCombos(*combosPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading combos: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[combos] loaded %d account(s) from %s\n", len(combos), *combosPath)
	} else {
		combos = []Combo{{}}
	}

	okCount := 0
	failCount := 0
	keepAlive := false

	for i, combo := range combos {
		if !*skipLogin {
			fmt.Printf("\n======== [%d/%d] %s ========\n", i+1, len(combos), combo.Email)
		}

		_, client, result, code, err := runCombo(runOptions{
			target:    *target,
			skipLogin: *skipLogin,
			doBasket:  doBasket,
			filtered:  filtered,
			maxQty:    *maxQty,
			combo:     combo,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] failed: %v\n", combo.Email, err)
			failCount++
			continue
		}
		if code != 0 {
			fmt.Fprintf(os.Stderr, "[%s] queue exited with code %d\n", combo.Email, code)
			failCount++
			continue
		}

		heldForDiscord := false
		if result != nil && result.Basket != nil {
			printPostQueueResult(result)
			if notifyPostQueueSuccess(combo.Email, result, client) {
				heldForDiscord = true
			}
			keepAlive = true
			if *openBrowser {
				cartURL := result.Basket.CartURL
				if cartURL == "" {
					cartURL = shopCartURL()
				}
				fmt.Println("\n======== step 5: open cart in browser ========")
				if err := client.OpenCartInBrowser(cartURL); err != nil {
					fmt.Fprintf(os.Stderr, "[browser] warning: %v\n", err)
				}
			}
			okCount++
		} else if result != nil && result.Login != nil {
			okCount++
		} else if *skipLogin {
			okCount++
		}

		if !heldForDiscord && client != nil {
			client.Close()
		}
	}

	if !*skipLogin {
		fmt.Printf("\n======== done: %d ok, %d failed, %d total ========\n", okCount, failCount, len(combos))
	}
	if keepAlive {
		waitAfterDiscord(true)
		return
	}
	if failCount > 0 {
		os.Exit(1)
	}
}
