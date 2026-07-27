package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Hyper-Solutions/hyper-sdk-go/v2"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
)

var scriptSrcRe = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+)["']`)

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

// postHyperSensorOnce posts exactly one Hyper sensor for pageURL.
// If html is non-nil, skips the page GET.
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
		if strings.Contains(r.Body, "/.well-known/sbsd") || strings.Contains(r.Body, "sbsd?") {
			return "", fmt.Errorf("SBSD challenge page — not handled")
		}
		body = r.Body
		pageURL = r.URL
	} else {
		body = *html
	}

	if isAkamaiInterstitial(body) {
		fmt.Println("[akamai] interstitial page — using BM script only (skip t= challenge)")
	}

	scriptURLs := akamaiScriptURLs(body, pageURL)
	if len(scriptURLs) == 0 {
		abck := cookieValue(session, "_abck", "")
		fmt.Printf("[akamai] no BM script path; abck len=%d (skip Hyper)\n", len(abck))
		return body, nil
	}

	scriptURL := scriptURLs[0]
	if strings.Contains(scriptURL, "t=") {
		tail := scriptURL
		if len(tail) > 50 {
			tail = tail[len(tail)-50:]
		}
		fmt.Printf("[akamai] refusing challenge script ...%s (skip Hyper)\n", tail)
		return body, nil
	}

	sr, err := session.do("GET", scriptURL, chromeHeaders(map[string]string{"referer": pageURL}, "script"), nil, 0)
	if err != nil {
		return body, err
	}
	tail := scriptURL
	if len(tail) > 60 {
		tail = tail[len(tail)-60:]
	}
	fmt.Printf("[akamai] script ...%s -> %d len=%d\n", tail, sr.StatusCode, len(sr.Body))

	abck := cookieValue(session, "_abck", "")
	bmsz := cookieValue(session, "bm_sz", "")
	p := fp()
	sensor, _, err := hyperSession.GenerateSensorData(context.Background(), &hyper.SensorInput{
		Abck:           abck,
		Bmsz:           bmsz,
		Version:        "3",
		PageUrl:        pageURL,
		UserAgent:      p.UA,
		IP:             ip,
		AcceptLanguage: p.AcceptLang,
		Context:        "",
		Script:         sr.Body,
		ScriptUrl:      scriptURL,
	})
	if err != nil {
		fmt.Printf("[akamai] Hyper sensor skipped: %v\n", err)
		return body, nil
	}

	payload, _ := json.Marshal(map[string]string{"sensor_data": sensor})
	pr, err := session.do("POST", scriptURL, chromeHeaders(map[string]string{"referer": pageURL}, "post_sensor"), strings.NewReader(string(payload)), 0)
	if err != nil {
		fmt.Printf("[akamai] Hyper sensor skipped: %v\n", err)
		return body, nil
	}
	abck = cookieValue(session, "_abck", "")
	fmt.Printf("[akamai] sensor[1] post=%d abck len=%d has_~0~=%v\n",
		pr.StatusCode, len(abck), strings.Contains(abck, "~0~"))
	return body, nil
}

func solveAkamai(session *Session, hyperSession *hyper.Session, pageURL, ip string) (string, error) {
	return postHyperSensorOnce(session, hyperSession, pageURL, ip, nil)
}
