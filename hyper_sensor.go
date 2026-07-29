package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Hyper-Solutions/hyper-sdk-go/v2"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
)

var (
	scriptSrcRe     = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+)["']`)
	sbsdChallengeRe = regexp.MustCompile(`(?i)["'](/[^"']+\?v=[0-9a-fA-F-]{36}(?:&|&amp;)t=\d+)["']`)
)

// Hyper docs: post sensors until _abck is valid (~0~), typically ~3.
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

func followToQueue(session *Session, startURL string) (finalURL, html string, err error) {
	u := startURL
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
		if step == 0 && strings.Contains(finalURL, "tickets.la28.org") && !isQueueURL(finalURL) {
			u = defaultTarget
			continue
		}
		break
	}
	return finalURL, html, nil
}

// enrichAndEnterQueue: SBSD challenge (if any) → sensors until valid → pixel → queue.
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
	fmt.Printf("[enrich] queue trigger %s (sensors=%d pixel=%v)\n", trigger, sensorsPosted, pixelOK)

	finalURL, qhtml, err := followToQueue(session, trigger)
	if err != nil {
		return "", "", err
	}
	if isSBSDChallengePage(qhtml) {
		if _, err := solveSBSDChallenge(session, hyperSession, qhtml, finalURL, ip); err != nil {
			return "", "", err
		}
		finalURL, qhtml, err = followToQueue(session, trigger)
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

// postHyperSensorOnce posts one Hyper sensor for pageURL unless _abck is already valid.
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
		fmt.Println("[akamai] _abck already valid — skip sensor")
		return body, nil
	}

	state := &sensorState{}
	if _, err := solveSensors(session, hyperSession, body, pageURL, ip, 1, state); err != nil {
		fmt.Printf("[akamai] Hyper sensor skipped: %v\n", err)
		return body, nil
	}
	return body, nil
}
