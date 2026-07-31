package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

type Client struct {
	TargetURL    string
	PollInterval time.Duration

	http     tls_client.HttpClient
	jar      tls_client.CookieJar
	hyper    *hyper.Session
	hyperKey string
	ip       string
	fp       Fingerprint

	sensorContext  string
	sensorScript   string
	sensorEndpoint string
	lastSensorData string

	queueConfig         *QueueConfig
	queueItemHeader     string
	queueitEnqueueToken string
}

func NewClient(hyperAPIKey, targetURL, proxyURL string, pollInterval time.Duration) (*Client, error) {
	if targetURL == "" {
		targetURL = defaultTarget
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	fp := currentFingerprint()
	jar := tls_client.NewCookieJar()
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(fp.TLSProfile),
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithDisableHttp3(),
		// Real Chrome uses HTTP/2. Forced H1 diverges from browser ATC and
		// correlates with CapacityControl soft-rejects (-3027) even when PUT is 200.
		tls_client.WithCookieJar(jar),
	}
	if proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}

	httpClient, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}

	c := &Client{
		TargetURL:    targetURL,
		PollInterval: pollInterval,
		http:         httpClient,
		jar:          jar,
		hyper:        hyper.NewSession(hyperAPIKey),
		hyperKey:     hyperAPIKey,
		fp:           fp,
	}
	c.ip = c.getPublicIP()
	fmt.Printf("[client] fingerprint: Chrome/%d Windows (TLS %s), egress IP for Hyper sensors: %s\n",
		fp.ChromeMajor, fp.TLSName, c.ip)
	return c, nil
}

func (c *Client) Close() {}

func (c *Client) getPublicIP() string {
	// Hyper requires the IP passed to sensor generation to match the egress IP.
	// ip.hypersolutions.co returns JSON {"ip":"..."} and needs x-api-key.
	if c.hyperKey != "" {
		headers := http.Header{
			"x-api-key": {c.hyperKey},
			"accept":    {"application/json"},
		}
		if resp, err := c.do("GET", "https://ip.hypersolutions.co/ip", headers, nil, true); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var obj struct {
				IP string `json:"ip"`
			}
			if json.Unmarshal(b, &obj) == nil {
				if ip := strings.TrimSpace(obj.IP); ip != "" {
					return ip
				}
			}
		}
	}
	if resp, err := c.do("GET", "https://api.ipify.org", nil, nil, true); err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ip := strings.TrimSpace(string(b))
		if ip != "" && !strings.Contains(ip, " ") && !strings.Contains(ip, "<") && !strings.HasPrefix(ip, "{") {
			return ip
		}
	}
	return "127.0.0.1"
}

func (c *Client) cookie(name string) string {
	candidates := []string{c.sensorEndpoint, baseURL, "https://tickets.la28.org/", "https://tickets.la28.org"}
	seen := map[string]bool{}
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		uu, err := url.Parse(raw)
		if err != nil || uu.Host == "" || seen[uu.Host] {
			continue
		}
		seen[uu.Host] = true
		for _, ck := range c.http.GetCookies(uu) {
			if ck.Name == name && ck.Value != "" {
				return ck.Value
			}
		}
	}
	return ""
}

func (c *Client) setCookie(name, value, domain string) {
	hosts := []string{}
	switch {
	case strings.HasPrefix(domain, "."):
		hosts = append(hosts, "https://www"+domain, "https://"+strings.TrimPrefix(domain, "."))
	default:
		hosts = append(hosts, "https://"+domain)
	}
	for _, h := range hosts {
		u, err := url.Parse(h)
		if err != nil {
			continue
		}
		c.http.SetCookies(u, []*http.Cookie{{
			Name:   name,
			Value:  value,
			Domain: domain,
			Path:   "/",
		}})
	}
}

func (c *Client) navHeaders(referer string) http.Header {
	site := "same-origin"
	if referer == "" {
		site = "none"
		referer = c.TargetURL
	}
	h := http.Header{
		"sec-ch-ua":                 {c.fp.SecChUA},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {c.fp.Platform},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {c.fp.UserAgent},
		"accept": {"text/html,application/xhtml+xml,application/xml;q=0.9," +
			"image/avif,image/webp,image/apng,*/*;q=0.8," +
			"application/signed-exchange;v=b3;q=0.7"},
		"sec-fetch-site":  {site},
		"sec-fetch-mode":  {"navigate"},
		"sec-fetch-user":  {"?1"},
		"sec-fetch-dest":  {"document"},
		"referer":         {referer},
		"accept-encoding": {"gzip, deflate, br, zstd"},
		"accept-language": {"en-US,en;q=0.9"},
		"priority":        {"u=0, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
	return h
}

func (c *Client) apiHeaders(referer string, extra map[string]string) http.Header {
	h := http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {c.fp.Platform},
		"user-agent":         {c.fp.UserAgent},
		"accept":             {"application/json, text/javascript, */*; q=0.01"},
		"content-type":       {"application/json"},
		"x-requested-with":   {"XMLHttpRequest"},
		"origin":             {baseURL},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {referer},
		"accept-encoding":    {"gzip, deflate, br"},
		"accept-language":    {"en-US,en;q=0.9"},
		"priority":           {"u=1, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"user-agent", "accept", "content-type", "x-xsrf-token", "x-requested-with",
			"origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

// atcPutHeaders matches CapacityControl's PUT /api/shoppingCart ajax call:
// content-type JSON + XHR, token only in the query string (no x-xsrf-token header).
// Do not add high-entropy client-hint headers here — they caused Akamai #18.
func (c *Client) atcPutHeaders(referer string) http.Header {
	return http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {c.fp.Platform},
		"user-agent":         {c.fp.UserAgent},
		"accept":             {"*/*"},
		"content-type":       {"application/json"},
		"x-requested-with":   {"XMLHttpRequest"},
		"origin":             {baseURL},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {referer},
		"accept-encoding":    {"gzip, deflate, br"},
		"accept-language":    {"en-US,en;q=0.9"},
		"priority":           {"u=1, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"user-agent", "accept", "content-type", "x-requested-with",
			"origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
}

// apiGetHeaders is for JSON GETs (cart poll/warm). Content-Type must not be set.
func (c *Client) apiGetHeaders(referer string, extra map[string]string) http.Header {
	h := http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {c.fp.Platform},
		"user-agent":         {c.fp.UserAgent},
		"accept":             {"application/json, text/javascript, */*; q=0.01"},
		"x-requested-with":   {"XMLHttpRequest"},
		"origin":             {baseURL},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {referer},
		"accept-encoding":    {"gzip, deflate, br"},
		"accept-language":    {"en-US,en;q=0.9"},
		"priority":           {"u=1, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"user-agent", "accept", "x-xsrf-token", "x-requested-with",
			"origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

// ssoGetHeaders is a GET-only header set for /api/singleSignOn.
// Content-Type must NOT be set — Akamai returns 403 Access Denied if it is.
func (c *Client) ssoGetHeaders(referer, xsrf string) http.Header {
	return http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {c.fp.Platform},
		"user-agent":         {c.fp.UserAgent},
		"accept":             {"application/json, text/plain, */*"},
		"x-xsrf-token":       {xsrf},
		"x-requested-with":   {"XMLHttpRequest"},
		"origin":             {baseURL},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {referer},
		"accept-encoding":    {"gzip, deflate, br"},
		"accept-language":    {"en-US,en;q=0.9"},
		"priority":           {"u=1, i"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"user-agent", "accept", "x-xsrf-token", "x-requested-with",
			"origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language", "priority",
		},
	}
}

type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	URL        string
	Text       string
}

func (c *Client) do(method, rawURL string, headers http.Header, body io.Reader, followRedirect bool) (*http.Response, error) {
	c.http.SetFollowRedirect(followRedirect)
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		req.Header = headers
	}
	return c.http.Do(req)
}

func (c *Client) request(method, rawURL string, headers http.Header, body io.Reader, followRedirect bool) (*httpResult, error) {
	resp, err := c.do(method, rawURL, headers, body, followRedirect)
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
	return &httpResult{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       b,
		URL:        finalURL,
		Text:       string(b),
	}, nil
}

func (c *Client) get(rawURL string, headers http.Header, followRedirect bool) (*httpResult, error) {
	return c.request("GET", rawURL, headers, nil, followRedirect)
}

func (c *Client) post(rawURL string, headers http.Header, body io.Reader, followRedirect bool) (*httpResult, error) {
	return c.request("POST", rawURL, headers, body, followRedirect)
}

func (c *Client) put(rawURL string, headers http.Header, body io.Reader, followRedirect bool) (*httpResult, error) {
	return c.request("PUT", rawURL, headers, body, followRedirect)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstQuery(u *url.URL, key string) string {
	return u.Query().Get(key)
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}

func headerGet(h http.Header, key string) string {
	if v := h.Get(key); v != "" {
		return v
	}
	return h.Get(strings.ToLower(key))
}

func fmtErr(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
