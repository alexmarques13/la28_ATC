package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

const (
	loginEmail = "blueeagle_123@outlook.com"
	loginPass  = "blueBLUE1234!@#$"

	gigyaAPIKey   = "4_w4CcQ6tKu4jTeDPirnKxnA"
	gigyaClientID = "xSden-TmSiYYelKvu19SMyTv"
	gigyaOrigin   = "https://la28id.la28.org"
	gigyaAPIHost  = "https://la28id.la28id.la28.org"

	recaptchaEnterpriseKey = "6Lc8WkwhAAAAAPHXbaEde5PP3Skj9tCZn-A8U555"

	loginPagePath = "/login/index.html"
	proxyPagePath = "/proxy.html"
)

// completeLogin runs Gigya accounts.login after queue and persists the authenticated session.
func completeLogin(session *Session, landingURL, landingHTML, twocaptchaKey string) error {
	fmt.Println("[login] starting Gigya SSO login flow")

	loginURL, oidcContext, err := navigateToLogin(session, landingURL, landingHTML)
	if err != nil {
		return err
	}
	fmt.Printf("[login] page=%s context_len=%d\n", truncStr(loginURL, 120), len(oidcContext))

	if err := gigyaBootstrap(session, loginURL); err != nil {
		return err
	}

	captchaToken, err := twocaptchaRecaptchaEnterprise(loginURL, recaptchaEnterpriseKey, twocaptchaKey)
	if err != nil {
		return fmt.Errorf("login captcha: %w", err)
	}

	loginResp, err := accountsLogin(session, loginURL, captchaToken)
	if err != nil {
		return err
	}
	loginToken := extractLoginToken(loginResp)
	uid, _ := loginResp["UID"].(string)
	fmt.Printf("[login] accounts.login ok UID=%s login_token_len=%d\n", truncStr(uid, 16), len(loginToken))

	finalURL, err := finishOIDC(session, loginURL, oidcContext, loginToken)
	if err != nil {
		fmt.Printf("[login] OIDC continue warning: %v (Gigya session still saved)\n", err)
	} else {
		fmt.Printf("[login] OIDC done -> %s\n", truncStr(finalURL, 140))
	}

	return saveLoginSession(session, finalURL, loginResp)
}

func navigateToLogin(session *Session, landingURL, landingHTML string) (loginURL, context string, err error) {
	if isGigyaLoginURL(landingURL) {
		return landingURL, contextFromURL(landingURL), nil
	}
	if u := extractGigyaLoginHref(landingHTML); u != "" {
		r, err := session.do("GET", u, chromeHeaders(map[string]string{
			"referer": landingURL,
		}, "navigate"), nil, 0)
		if err != nil {
			return "", "", err
		}
		if isGigyaLoginURL(r.URL) {
			return r.URL, contextFromURL(r.URL), nil
		}
		landingURL, landingHTML = r.URL, r.Body
	}

	// Eventim Keycloak/SSO gate: forceLogin=true triggers IdP redirect.
	candidates := []string{}
	if landingURL != "" {
		candidates = append(candidates, withQuery(landingURL, "forceLogin", "true"))
	}
	candidates = append(candidates,
		baseURL+"/mycustomerdata/?forceLogin=true",
		baseURL+"/?forceLogin=true",
	)

	for _, start := range candidates {
		fmt.Printf("[login] SSO navigate %s\n", truncStr(start, 100))
		final, hops, err := followRedirects(session, start, landingURL, 14)
		if err != nil {
			fmt.Printf("[login] navigate err: %v\n", err)
			continue
		}
		for _, h := range hops {
			if ctx := contextFromURL(h); ctx != "" {
				context = ctx
			}
			if isGigyaLoginURL(h) {
				return h, contextFromURL(h), nil
			}
		}
		if isGigyaLoginURL(final.URL) {
			if context == "" {
				context = contextFromURL(final.URL)
			}
			return final.URL, context, nil
		}
		if strings.Contains(final.URL, "la28id.la28.org") {
			if ctx := contextFromURL(final.URL); ctx != "" {
				context = ctx
			}
			// May have landed on proxy/error — still try default login URL with client id.
		}
	}

	loginURL = fmt.Sprintf("%s%s?gig_ui_locales=en&gig_client_id=%s", gigyaOrigin, loginPagePath, gigyaClientID)
	if context != "" {
		loginURL += "&context=" + url.QueryEscape(context)
	}
	r, err := session.do("GET", loginURL, chromeHeaders(map[string]string{
		"referer": baseURL + "/",
	}, "navigate"), nil, 0)
	if err != nil {
		return "", "", err
	}
	return r.URL, firstNonEmpty(context, contextFromURL(r.URL)), nil
}

func gigyaBootstrap(session *Session, pageURL string) error {
	seedGigyaCookies(session)

	boot := gigyaAPIHost + "/accounts.webSdkBootstrap?" + url.Values{
		"apiKey":  {gigyaAPIKey},
		"pageURL": {pageURL},
		"sdk":     {"js_latest"},
		"format":  {"json"},
	}.Encode()

	hdrs := chromeHeaders(map[string]string{
		"accept":         "*/*",
		"origin":         gigyaOrigin,
		"referer":        pageURL,
		"sec-fetch-site": "same-site",
		"sec-fetch-mode": "cors",
		"sec-fetch-dest": "empty",
	}, "xhr")
	// Drop JSON content-type / x-requested-with for bootstrap GET
	hdrs = filterHeaders(hdrs, map[string]string{
		"content-type":     "",
		"x-requested-with": "",
		"accept":           "*/*",
	})

	r, err := session.do("GET", boot, hdrs, nil, 0)
	if err != nil {
		return err
	}
	fmt.Printf("[login] bootstrap %d\n", r.StatusCode)

	// Ensure login document cookies (Akamai on id domain).
	_, _ = session.do("GET", pageURL, chromeHeaders(map[string]string{
		"referer": gigyaOrigin + "/",
	}, "navigate"), nil, 0)
	return nil
}

func accountsLogin(session *Session, pageURL, captchaToken string) (map[string]any, error) {
	p := fp()
	_, offset := time.Now().Zone()
	risk, _ := json.Marshal(map[string]any{
		"b0": 8546,
		"b1": []int{117, 125, 116, 83},
		"b2": 8,
		"b3": []any{},
		"b4": 2,
		"b5": 1,
		"b6": p.UA,
		"b7": []map[string]any{
			{"name": "PDF Viewer", "filename": "internal-pdf-viewer", "length": 2},
			{"name": "Chrome PDF Viewer", "filename": "internal-pdf-viewer", "length": 2},
			{"name": "Chromium PDF Viewer", "filename": "internal-pdf-viewer", "length": 2},
			{"name": "Microsoft Edge PDF Viewer", "filename": "internal-pdf-viewer", "length": 2},
			{"name": "WebKit built-in PDF", "filename": "internal-pdf-viewer", "length": 2},
		},
		"b8":  time.Now().Format("3:04:05 PM"),
		"b9":  -offset / 60,
		"b10": map[string]any{"state": "prompt"},
		"b11": false,
		"b12": map[string]any{"charging": true, "chargingTime": 0, "dischargingTime": nil, "level": 1},
		"b13": []any{nil, strings.ReplaceAll(p.Screen, " x ", "|") + "|24", false, true},
	})

	form := url.Values{}
	form.Set("loginID", loginEmail)
	form.Set("password", loginPass)
	form.Set("sessionExpiration", "14400")
	form.Set("targetEnv", "jssdk")
	form.Set("include", "profile,data,emails,subscriptions,preferences,")
	form.Set("includeUserInfo", "true")
	form.Set("captchaToken", captchaToken)
	form.Set("captchaType", "reCaptchaEnterpriseScore")
	form.Set("loginMode", "standard")
	form.Set("lang", "en")
	form.Set("riskContext", string(risk))
	form.Set("APIKey", gigyaAPIKey)
	form.Set("source", "showScreenSet")
	form.Set("sdk", "js_latest")
	form.Set("authMode", "cookie")
	form.Set("pageURL", pageURL)
	form.Set("sdkBuild", "2231")
	form.Set("format", "json")

	endpoint := gigyaAPIHost + "/accounts.login"
	hdrs := []headerKV{
		{"sec-ch-ua", p.SecChUA},
		{"sec-ch-ua-mobile", "?0"},
		{"sec-ch-ua-platform", fmt.Sprintf(`"%s"`, p.Platform)},
		{"user-agent", p.UA},
		{"accept", "*/*"},
		{"content-type", "application/x-www-form-urlencoded"},
		{"origin", gigyaOrigin},
		{"sec-fetch-site", "same-site"},
		{"sec-fetch-mode", "cors"},
		{"sec-fetch-dest", "empty"},
		{"referer", gigyaOrigin + "/"},
		{"accept-language", p.AcceptLang},
	}

	r, err := session.do("POST", endpoint, hdrs, strings.NewReader(form.Encode()), 0)
	if err != nil {
		return nil, err
	}
	preview := r.Body
	if len(preview) > 280 {
		preview = preview[:280]
	}
	fmt.Printf("[login] accounts.login %d %s\n", r.StatusCode, preview)

	var data map[string]any
	if err := json.Unmarshal([]byte(r.Body), &data); err != nil {
		return nil, fmt.Errorf("accounts.login JSON: %w body=%s", err, preview)
	}
	if code, _ := data["errorCode"].(float64); code != 0 {
		return nil, fmt.Errorf("accounts.login errorCode=%v msg=%v details=%v",
			data["errorCode"], data["errorMessage"], data["errorDetails"])
	}
	return data, nil
}

func finishOIDC(session *Session, loginURL, oidcContext, loginToken string) (string, error) {
	if loginToken == "" {
		loginToken = cookieValueHost(session, gigyaAPIHost+"/", "glt_"+gigyaAPIKey, "")
		if loginToken == "" {
			loginToken = cookieValueHost(session, gigyaOrigin+"/", "glt_"+gigyaAPIKey, "")
		}
	}

	gmidTicket, _ := fetchGmidTicket(session, loginURL)

	if oidcContext == "" {
		oidcContext = cookieValueHost(session, gigyaOrigin+"/", "gig_oidcContext_"+gigyaAPIKey, "")
	}
	if oidcContext == "" {
		oidcContext = cookieValueHost(session, gigyaAPIHost+"/", "gig_oidcContext_"+gigyaAPIKey, "")
	}

	// Prefer native authorize/continue when we have context + login token.
	if oidcContext != "" && loginToken != "" {
		q := url.Values{}
		q.Set("context", oidcContext)
		q.Set("login_token", loginToken)
		q.Set("client_id", gigyaClientID)
		if gmidTicket != "" {
			q.Set("gmidTicket", gmidTicket)
		}
		cont := fmt.Sprintf("%s/oidc/op/v1.0/%s/authorize/continue?%s", gigyaAPIHost, gigyaAPIKey, q.Encode())
		fmt.Printf("[login] authorize/continue context_len=%d\n", len(oidcContext))
		final, _, err := followRedirects(session, cont, gigyaOrigin+proxyPagePath+"?mode=afterLogin", 16)
		if err != nil {
			return "", err
		}
		if strings.Contains(final.URL, "errorMessage") || strings.Contains(final.URL, "mode=error") {
			return final.URL, fmt.Errorf("OIDC continue error page: %s", truncStr(final.URL, 180))
		}
		return final.URL, nil
	}

	// Fallback: already logged into Gigya — re-enter tickets SSO so authorize can mint a code.
	fmt.Println("[login] no OIDC context; retry tickets forceLogin with Gigya session")
	final, hops, err := followRedirects(session, baseURL+"/mycustomerdata/?forceLogin=true", gigyaOrigin+"/", 16)
	if err != nil {
		return "", err
	}
	for _, h := range hops {
		fmt.Printf("[login] post-login hop %s\n", truncStr(h, 140))
		if strings.Contains(h, "code=") && strings.Contains(h, "tickets.la28.org") {
			return h, nil
		}
	}
	if final != nil && !strings.Contains(final.URL, "softblock") && !strings.Contains(final.URL, "/queue/") {
		return final.URL, nil
	}
	if final != nil {
		return final.URL, fmt.Errorf("tickets SSO did not complete (landed %s)", truncStr(final.URL, 120))
	}
	return "", fmt.Errorf("tickets SSO did not complete")
}

func fetchGmidTicket(session *Session, referer string) (string, error) {
	u := gigyaAPIHost + "/socialize.getGmidTicket?" + url.Values{
		"apiKey": {gigyaAPIKey},
		"format": {"json"},
	}.Encode()
	hdrs := chromeHeaders(map[string]string{
		"accept":         "*/*",
		"origin":         gigyaOrigin,
		"referer":        referer,
		"sec-fetch-site": "same-site",
	}, "xhr")
	hdrs = filterHeaders(hdrs, map[string]string{"content-type": "", "x-requested-with": ""})
	r, err := session.do("GET", u, hdrs, nil, 0)
	if err != nil {
		return "", err
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(r.Body), &data); err != nil {
		return "", err
	}
	t, _ := data["gmidTicket"].(string)
	return t, nil
}

func saveLoginSession(session *Session, finalURL string, loginResp map[string]any) error {
	payload := map[string]any{
		"url":     finalURL,
		"uid":     loginResp["UID"],
		"profile": loginResp["profile"],
		"cookies": allCookiesMulti(session,
			baseURL+"/",
			gigyaOrigin+"/",
			gigyaAPIHost+"/",
		),
		"savedAt": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile("login_session.json", raw, 0o644); err != nil {
		return err
	}
	fmt.Println("[login] wrote login_session.json")
	return nil
}

func followRedirects(session *Session, startURL, referer string, maxHops int) (*HTTPResult, []string, error) {
	client, err := session.noRedirectClient()
	if err != nil {
		return nil, nil, err
	}
	p := fp()
	cur := startURL
	ref := referer
	hops := []string{cur}
	var last *HTTPResult

	for i := 0; i < maxHops; i++ {
		req, err := http.NewRequest("GET", cur, nil)
		if err != nil {
			return nil, hops, err
		}
		hdrs := chromeHeaders(map[string]string{}, "navigate")
		if ref != "" {
			hdrs = chromeHeaders(map[string]string{"referer": ref}, "navigate")
		}
		order := make([]string, 0, len(hdrs))
		for _, kv := range hdrs {
			req.Header.Set(kv.Key, kv.Value)
			order = append(order, strings.ToLower(kv.Key))
		}
		_ = p
		req.Header[http.HeaderOrderKey] = order

		resp, err := client.Do(req)
		if err != nil {
			return nil, hops, err
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = &HTTPResult{StatusCode: resp.StatusCode, URL: cur, Body: string(b), Header: resp.Header}
		fmt.Printf("[login:hop] %d %d %s\n", i, resp.StatusCode, truncStr(cur, 140))

		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return last, hops, nil
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return last, hops, nil
		}
		next, err := url.Parse(loc)
		if err != nil {
			return last, hops, err
		}
		base, _ := url.Parse(cur)
		abs := base.ResolveReference(next).String()
		ref = cur
		cur = abs
		hops = append(hops, cur)
	}
	return last, hops, fmt.Errorf("too many redirects")
}

func seedGigyaCookies(session *Session) {
	u, _ := url.Parse(gigyaOrigin + "/")
	session.Client.SetCookies(u, []*http.Cookie{{
		Name:   "gig_bootstrap_" + gigyaAPIKey,
		Value:  "la28id_ver4",
		Domain: "la28id.la28.org",
		Path:   "/",
	}})
}

func extractLoginToken(resp map[string]any) string {
	if si, ok := resp["sessionInfo"].(map[string]any); ok {
		if t, ok := si["sessionToken"].(string); ok && t != "" {
			return t
		}
		if t, ok := si["login_token"].(string); ok && t != "" {
			return t
		}
	}
	if t, ok := resp["sessionToken"].(string); ok {
		return t
	}
	return ""
}

func isGigyaLoginURL(u string) bool {
	lu := strings.ToLower(u)
	return strings.Contains(lu, "la28id.la28.org") && strings.Contains(lu, "/login")
}

func contextFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"context", "oidc_context", "gig_context"} {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	if u.Fragment != "" {
		fq, err := url.ParseQuery(strings.TrimPrefix(u.Fragment, "#"))
		if err == nil {
			for _, k := range []string{"context", "oidc_context", "gig_context"} {
				if v := fq.Get(k); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func extractGigyaLoginHref(html string) string {
	re := regexp.MustCompile(`(?i)https://la28id\.la28\.org/[^"'\\\s]+`)
	if m := re.FindString(html); m != "" {
		return m
	}
	re2 := regexp.MustCompile(`(?i)href=["']([^"']*la28id\.la28\.org[^"']*)["']`)
	if m := re2.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	return ""
}

func withQuery(raw, key, val string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set(key, val)
	u.RawQuery = q.Encode()
	return u.String()
}

func cookieValueHost(session *Session, hostURL, name, defaultVal string) string {
	u, err := url.Parse(hostURL)
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

func filterHeaders(hdrs []headerKV, overrides map[string]string) []headerKV {
	out := make([]headerKV, 0, len(hdrs))
	for _, h := range hdrs {
		lk := strings.ToLower(h.Key)
		if v, ok := overrides[lk]; ok {
			if v == "" {
				continue
			}
			out = append(out, headerKV{h.Key, v})
			continue
		}
		out = append(out, h)
	}
	for k, v := range overrides {
		if v == "" {
			continue
		}
		found := false
		for _, h := range out {
			if strings.EqualFold(h.Key, k) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, headerKV{k, v})
		}
	}
	return out
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
