package main

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

const (
	baseURL       = "https://tickets.la28.org"
	defaultTarget = baseURL + "/mycustomerdata/"
	navAccept     = "text/html,application/xhtml+xml,application/xml;q=0.9," +
		"image/avif,image/webp,image/apng,*/*;q=0.8," +
		"application/signed-exchange;v=b3;q=0.7"
)

// Session wraps a tls-client HttpClient for this run.
type Session struct {
	Client tls_client.HttpClient
}

func makeSession() (*Session, error) {
	jar := tls_client.NewCookieJar()
	client, err := tls_client.NewHttpClient(
		tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(45),
		tls_client.WithClientProfile(clientProfileFor(fp().Impersonate)),
		tls_client.WithCookieJar(jar),
	)
	if err != nil {
		return nil, err
	}
	return &Session{Client: client}, nil
}

func publicIP(session *Session) (string, error) {
	resp, err := session.do("GET", "https://api.ipify.org", chromeHeaders(map[string]string{"accept": "*/*"}, "navigate"), nil, 20*time.Second)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ipify HTTP %d", resp.StatusCode)
	}
	return strings.TrimSpace(resp.Body), nil
}

// chromeHeaders builds Chrome-like headers. kind: navigate|script|post_sensor|xhr
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
	default: // xhr
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

// HTTPResult is a simplified response after redirects.
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
		// Per-request timeout is not exposed; client already has WithTimeoutSeconds(45).
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
	u, _ := url.Parse(baseURL + "/")
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

// quoteLikePython mirrors urllib.parse.quote(s, safe="") — %20 for spaces.
func quoteLikePython(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// encodeQuery preserves insertion order and uses %20 for spaces (like quote_via=quote).
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
