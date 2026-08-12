package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

func (c *Client) extractXSRF(html string) (string, error) {
	for _, m := range reXSRF.FindAllStringSubmatch(html, -1) {
		if len(m) < 2 {
			continue
		}
		tok := m[1]
		if tok == "" || strings.Contains(tok, "PLACEHOLDER") {
			continue
		}
		return tok, nil
	}
	if tok := c.webshopToken(shopAffiliate); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("xsrfToken not found on page")
}

func (c *Client) webshopToken(affiliate string) string {
	u := mustParseURL(baseURL + "/")
	name := affiliate + ".webshop"
	for _, ck := range c.http.GetCookies(u) {
		if ck.Name != name {
			continue
		}
		raw, err := decodeWebshop(ck.Value)
		if err != nil {
			return ""
		}
		vals, _ := url.ParseQuery(raw)
		if t := vals.Get("token"); t != "" {
			return t
		}
	}
	return ""
}

func (c *Client) syncWebshopToken(affiliate, xsrf string) {
	_ = c.ensureWebshopATCToken(affiliate, "", xsrf)
}

func (c *Client) ensureWebshopATCToken(affiliate, pageHTML, cartXSRF string) string {
	if affiliate == "" {
		affiliate = shopAffiliate
	}

	tok := ""
	if cartXSRF != "" && !strings.Contains(cartXSRF, "PLACEHOLDER") {
		tok = cartXSRF
		fmt.Printf("[basket] webshop token (cart xsrf)=%s\n", truncate(tok, 20))
	}
	if tok == "" {
		if existing := c.webshopToken(affiliate); existing != "" {
			fmt.Printf("[basket] webshop token (existing)=%s\n", truncate(existing, 20))
			return existing
		}
	}
	if tok == "" {
		for _, m := range reXSRF.FindAllStringSubmatch(pageHTML, -1) {
			if len(m) < 2 {
				continue
			}
			cand := m[1]
			if cand != "" && !strings.Contains(cand, "PLACEHOLDER") {
				tok = cand
				fmt.Printf("[basket] webshop token (page xsrf)=%s\n", truncate(tok, 20))
				break
			}
		}
	}
	if tok == "" {
		const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		b := make([]byte, 15)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(26))
			if err != nil {
				b[i] = letters[i%26]
				continue
			}
			b[i] = letters[n.Int64()]
		}
		tok = string(b)
		fmt.Printf("[basket] webshop token (R 15-letter)=%s\n", tok)
	}

	u := mustParseURL(baseURL + "/")
	name := affiliate + ".webshop"
	raw := ""
	for _, ck := range c.http.GetCookies(u) {
		if ck.Name == name {
			if d, err := decodeWebshop(ck.Value); err == nil {
				raw = d
			}
			break
		}
	}
	vals, _ := url.ParseQuery(raw)
	vals.Set("token", tok)
	encoded := encodeWebshop(vals.Encode())
	c.http.SetCookies(u, []*http.Cookie{{
		Name:     name,
		Value:    encoded,
		Path:     "/",
		Domain:   "tickets.la28.org",
		HttpOnly: false,
		Secure:   true,
	}})
	c.http.SetCookies(mustParseURL("https://la28.org/"), []*http.Cookie{{
		Name:     name,
		Value:    encoded,
		Path:     "/",
		Domain:   "la28.org",
		HttpOnly: false,
		Secure:   true,
	}})
	return tok
}

func decodeWebshop(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(b64)
		if err != nil {
			return "", err
		}
	}
	return string(raw), nil
}

func encodeWebshop(query string) string {
	return base64.StdEncoding.EncodeToString([]byte(query))
}

func (c *Client) gigyaPost(method string, params map[string]string, referer string) (map[string]any, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	form.Set("apiKey", gigyaAPIKey)
	form.Set("format", "json")
	form.Set("httpStatusCodes", "true")

	headers := http.Header{
		"user-agent":      {c.fp.UserAgent},
		"content-type":    {"application/x-www-form-urlencoded"},
		"accept":          {"*/*"},
		"origin":          {"https://la28id.la28.org"},
		"referer":         {referer},
		"accept-language": {"en-US,en;q=0.9"},
		http.HeaderOrderKey: {
			"user-agent", "content-type", "accept", "origin", "referer", "accept-language",
		},
	}
	resp, err := c.post(gigyaDC+"/"+method, headers, strings.NewReader(form.Encode()), true)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, fmt.Errorf("gigya %s non-json: %d %s", method, resp.StatusCode, truncate(resp.Text, 300))
	}
	return data, nil
}

func (c *Client) Login(email, password string) (*LoginResult, error) {
	fmt.Printf("[login] starting SSO login for %s\n", email)

	pageResp, err := c.get(c.TargetURL, c.navHeaders(""), true)
	if err != nil {
		return nil, err
	}
	xsrf, err := c.extractXSRF(pageResp.Text)
	if err != nil {
		return nil, err
	}

	headers := c.apiHeaders(c.TargetURL, map[string]string{
		"x-xsrf-token": xsrf,
		"accept":       "application/json, text/plain, */*",
	})
	providersURL := baseURL + "/api/login/ssoProvider?" + url.Values{"redirectUrl": {c.TargetURL}}.Encode()
	provResp, err := c.get(providersURL, headers, true)
	if err != nil {
		return nil, err
	}
	var providers []map[string]any
	if err := json.Unmarshal(provResp.Body, &providers); err != nil || len(providers) == 0 {
		return nil, fmt.Errorf("no SSO providers returned")
	}
	provider := providers[0]
	authURL, _ := provider["authenticationUrl"].(string)
	esID := "857"
	switch v := provider["id"].(type) {
	case float64:
		esID = fmt.Sprintf("%.0f", v)
	case string:
		if v != "" {
			esID = v
		}
	}
	name, _ := provider["name"].(string)
	fmt.Printf("[login] provider=%s esId=%s\n", name, esID)

	proxyResp, err := c.get(authURL, c.navHeaders(c.TargetURL), true)
	if err != nil {
		return nil, err
	}
	if isQueueitURL(proxyResp.URL) || strings.Contains(proxyResp.URL, "/queue/") {
		fmt.Printf("[login] OIDC hit queue %s — passing\n", truncate(proxyResp.URL, 100))
		target := authURL
		if t := mustParseURL(proxyResp.URL).Query().Get("t"); t != "" {
			target = t
		}
		if _, qErr := c.JoinAndWaitForURL(target, 0); qErr != nil &&
			!strings.Contains(qErr.Error(), "no queue required") {
			_ = c.ensureQueueAccess(c.TargetURL)
		}
		proxyResp, err = c.get(authURL, c.navHeaders(c.TargetURL), true)
		if err != nil {
			return nil, err
		}
	}
	proxyQS := mustParseURL(proxyResp.URL).Query()
	context := proxyQS.Get("context")
	oidcClientID := proxyQS.Get("client_id")
	if oidcClientID == "" {
		oidcClientID = gigyaOIDCClientID
	}
	if context == "" {
		return nil, fmt.Errorf("OIDC context missing from %s", proxyResp.URL)
	}

	ids, err := c.gigyaPost("socialize.getIDs", map[string]string{
		"APIKey":        gigyaAPIKey,
		"includeTicket": "true",
	}, "https://la28id.la28.org/login/index.html")
	if err != nil {
		return nil, err
	}
	if code, ok := ids["errorCode"]; ok && code != nil && code != float64(0) {
		return nil, fmt.Errorf("socialize.getIDs failed: %v", ids)
	}
	gcid, _ := ids["gcid"].(string)
	ucid, _ := ids["ucid"].(string)
	gmidTicket, _ := ids["gmidTicket"].(string)

	login, err := c.gigyaPost("accounts.login", map[string]string{
		"loginID":           email,
		"password":          password,
		"targetEnv":         "browser",
		"sessionExpiration": "14400",
		"include":           "profile,data,emails,loginIDs",
		"captchaType":       "reCaptchaEnterpriseScore",
		"captchaToken":      "unused",
	}, "https://la28id.la28.org/login/index.html")
	if err != nil {
		return nil, err
	}
	if code, _ := login["errorCode"].(float64); code != 0 {
		return nil, fmt.Errorf("accounts.login failed: %v", login)
	}

	sessionInfo, _ := login["sessionInfo"].(map[string]any)
	loginToken, _ := sessionInfo["cookieValue"].(string)
	cookieName, _ := sessionInfo["cookieName"].(string)
	if cookieName == "" {
		cookieName = "gac_" + gigyaAPIKey
	}
	uid, _ := login["UID"].(string)
	fmt.Printf("[login] gigya ok UID=%s\n", uid)

	for _, pair := range [][2]string{
		{cookieName, loginToken},
		{"gmid", gcid},
		{"ucid", ucid},
		{"gig_gmid", gcid},
		{"gig_ucid", ucid},
	} {
		if pair[1] == "" {
			continue
		}
		for _, domain := range []string{".gigya.com", "la28id.la28.org", ".la28.org"} {
			c.setCookie(pair[0], pair[1], domain)
		}
	}

	continueURL := gigyaFIDM + "/authorize/continue?" + url.Values{
		"context":     {context},
		"login_token": {loginToken},
		"client_id":   {oidcClientID},
		"gmidTicket":  {gmidTicket},
		"DeviceId":    {gcid},
	}.Encode()

	cont, err := c.get(continueURL, c.navHeaders("https://la28id.la28.org/proxy.html"), false)
	if err != nil {
		return nil, err
	}
	location := headerGet(cont.Header, "Location")
	if location == "" || (cont.StatusCode < 301 || cont.StatusCode > 308) {
		return nil, fmt.Errorf("authorize/continue failed: %d %s", cont.StatusCode, truncate(cont.Text, 400))
	}
	if strings.HasPrefix(location, "/") {
		location = "https://fidm.eu1.gigya.com" + location
	}

	callback, err := c.get(location, c.navHeaders(continueURL), true)
	if err != nil {
		return nil, err
	}
	callbackURL := callback.URL
	fmt.Printf("[login] oauth callback: %s...\n", truncate(callbackURL, 160))

	cbq := mustParseURL(callbackURL).Query()
	code := cbq.Get("code")
	if code == "" {
		return nil, fmt.Errorf("oauth code missing from callback: %s", callbackURL)
	}

	state := cbq.Get("state")
	iss := cbq.Get("iss")
	stateParts := mustParseURL(c.TargetURL)
	if state != "" {
		stateParts = mustParseURL(state)
	}
	stateQ := stateParts.Query()
	if v := stateQ.Get("esId"); v != "" {
		esID = v
	}
	stateQ.Set("code", code)
	if iss != "" {
		stateQ.Set("iss", iss)
	}

	scheme := stateParts.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := stateParts.Host
	if host == "" {
		host = "tickets.la28.org"
	}
	if stateQ.Get("affiliate") == "" && cbq.Get("affiliate") != "" {
		stateQ.Set("affiliate", cbq.Get("affiliate"))
	}
	if got := stateQ.Get("affiliate"); got != "" {
		fmt.Printf("[login] oauth shop affiliate=%s (not overridden)\n", got)
	}
	frag := stateParts.Fragment
	if frag == "" {
		frag = "generic/,oauth"
	}
	landingURL := (&url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     stateParts.Path,
		RawQuery: stateQ.Encode(),
		Fragment: frag,
	}).String()

	shopPage, err := c.get(landingURL, c.navHeaders(callbackURL), true)
	if err != nil {
		return nil, err
	}
	xsrf, err = c.extractXSRF(shopPage.Text)
	if err != nil {
		return nil, err
	}
	referer := strings.Split(shopPage.URL, "#")[0]

	if err := c.prepareShopAkamai(referer, shopPage.Text); err != nil {
		fmt.Printf("[login] akamai prepare warning: %v\n", err)
	}

	ssoParams := url.Values{
		"code":          {code},
		"contenttype":   {"json"},
		"force_session": {"true"},
	}
	ssoURL := fmt.Sprintf("%s/api/singleSignOn/%s?%s", baseURL, esID, ssoParams.Encode())

	ssoResp, err := c.get(ssoURL, c.ssoGetHeaders(referer, xsrf), true)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[login] singleSignOn %d %s\n", ssoResp.StatusCode, truncate(ssoResp.Text, 300))

	if ssoResp.StatusCode == 403 {
		fmt.Println("[login] singleSignOn 403 — refreshing akamai and retrying")
		_ = c.postSensors(referer, false, 1)
		if page2, err := c.get(c.TargetURL, c.navHeaders(referer), true); err == nil {
			if x2, err := c.extractXSRF(page2.Text); err == nil {
				xsrf = x2
			}
		}
		ssoResp, err = c.get(ssoURL, c.ssoGetHeaders(referer, xsrf), true)
		if err != nil {
			return nil, err
		}
		fmt.Printf("[login] singleSignOn retry %d %s\n", ssoResp.StatusCode, truncate(ssoResp.Text, 300))
	}

	if ssoResp.StatusCode != 200 {
		return nil, fmt.Errorf("singleSignOn failed: %d %s", ssoResp.StatusCode, truncate(ssoResp.Text, 500))
	}
	var ssoData map[string]any
	if err := json.Unmarshal(ssoResp.Body, &ssoData); err != nil {
		return nil, fmt.Errorf("singleSignOn non-json: %s", truncate(ssoResp.Text, 500))
	}
	ssoCode, _ := ssoData["code"].(float64)
	codeInt := int(ssoCode)
	if codeInt < 1 || codeInt > 5 {
		return nil, fmt.Errorf("singleSignOn unexpected status: %v", ssoData)
	}
	fmt.Printf("[login] SSO status code=%d (3=CUSTOMER_LOGGED_IN)\n", codeInt)

	verifyAff := "28A"
	if a := c.webshopField("28A", "login"); a == "1" {
		verifyAff = "28A"
	} else if c.webshopField(shopAffiliate, "login") == "1" {
		verifyAff = shopAffiliate
	}
	verifyURL := fmt.Sprintf("%s/mycustomerdata/?affiliate=%s", baseURL, verifyAff)
	verify, err := c.get(verifyURL, c.navHeaders(c.TargetURL), true)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[login] done logged_in=True url=%s\n", verify.URL)
	c.logWebshopLoginState()

	if c.webshopField(shopAffiliate, "login") != "1" || strings.Contains(c.webidValue(shopAffiliate), "NO_SESSION") {
		if err := c.bootstrap28VSession(email, password); err != nil {
			c.logWebshopLoginState()
			return nil, fmt.Errorf("%s session required for ATC (else -3027): %w", shopAffiliate, err)
		}
		c.logWebshopLoginState()
	}

	ssoRaw, _ := json.Marshal(ssoData)
	return &LoginResult{
		UID:      uid,
		Email:    email,
		EsID:     esID,
		SSO:      ssoRaw,
		LoggedIn: true,
		FinalURL: verify.URL,
	}, nil
}

func (c *Client) bootstrap28VSession(email, password string) error {
	fmt.Println("[login] bootstrapping real 28V session via OIDC…")
	if email == "" || password == "" {
		return fmt.Errorf("email/password required to refresh Gigya session for 28V OIDC")
	}
	target := baseURL + "/mycustomerdata/?affiliate=" + shopAffiliate

	page, err := c.get(target, c.navHeaders(baseURL+"/"), true)
	if err != nil {
		return err
	}
	xsrf, err := c.extractXSRF(page.Text)
	if err != nil {
		xsrf = c.webshopToken(shopAffiliate)
		if xsrf == "" {
			xsrf = c.webshopToken("28A")
		}
	}
	headers := c.apiHeaders(target, map[string]string{
		"x-xsrf-token": xsrf,
		"accept":       "application/json, text/plain, */*",
	})
	providersURL := baseURL + "/api/login/ssoProvider?" + url.Values{"redirectUrl": {target}}.Encode()
	provResp, err := c.get(providersURL, headers, true)
	if err != nil {
		return err
	}
	var providers []map[string]any
	if err := json.Unmarshal(provResp.Body, &providers); err != nil || len(providers) == 0 {
		return fmt.Errorf("no SSO providers for 28V bootstrap: %s", truncate(provResp.Text, 200))
	}
	authURL, _ := providers[0]["authenticationUrl"].(string)
	esID := "857"
	switch v := providers[0]["id"].(type) {
	case float64:
		esID = fmt.Sprintf("%.0f", v)
	case string:
		if v != "" {
			esID = v
		}
	}
	if authURL == "" {
		return fmt.Errorf("empty authenticationUrl for 28V bootstrap")
	}

	authURL = rewriteAuthAffiliate(authURL, shopAffiliate)
	fmt.Printf("[login] 28V auth redirect_uri=%s\n", truncate(mustParseURL(authURL).Query().Get("redirect_uri"), 80))

	proxyResp, err := c.get(authURL, c.navHeaders(target), true)
	if err != nil {
		return err
	}
	if isQueueitURL(proxyResp.URL) || strings.Contains(proxyResp.URL, "/queue/") {
		fmt.Printf("[login] 28V OIDC hit queue %s — passing\n", truncate(proxyResp.URL, 100))
		_ = c.ensureQueueAccess(target)
		proxyResp, err = c.get(authURL, c.navHeaders(target), true)
		if err != nil {
			return err
		}
	}
	proxyQS := mustParseURL(proxyResp.URL).Query()
	fmt.Printf("[login] 28V OIDC land %s\n", truncate(proxyResp.URL, 160))
	if earlyCode := proxyQS.Get("code"); earlyCode != "" {
		fmt.Println("[login] 28V OIDC returned code without continue — completing SSO")
		return c.complete28VSSO(earlyCode, proxyQS.Get("iss"), esID, target, xsrf, proxyResp.URL)
	}
	if errMsg := proxyQS.Get("error"); errMsg != "" {
		return fmt.Errorf("28V OIDC authorize error=%s desc=%s url=%s",
			errMsg, proxyQS.Get("error_description"), truncate(proxyResp.URL, 160))
	}
	context := proxyQS.Get("context")
	oidcClientID := proxyQS.Get("client_id")
	if oidcClientID == "" {
		oidcClientID = gigyaOIDCClientID
	}
	if context == "" {
		return fmt.Errorf("OIDC context missing during 28V bootstrap: %s", truncate(proxyResp.URL, 160))
	}

	ids, err := c.gigyaPost("socialize.getIDs", map[string]string{
		"APIKey":        gigyaAPIKey,
		"includeTicket": "true",
	}, "https://la28id.la28.org/login/index.html")
	if err != nil {
		return err
	}
	gcid, _ := ids["gcid"].(string)
	ucid, _ := ids["ucid"].(string)
	gmidTicket, _ := ids["gmidTicket"].(string)

	login, err := c.gigyaPost("accounts.login", map[string]string{
		"loginID":           email,
		"password":          password,
		"targetEnv":         "browser",
		"sessionExpiration": "14400",
		"include":           "profile,data,emails,loginIDs",
		"captchaType":       "reCaptchaEnterpriseScore",
		"captchaToken":      "unused",
	}, "https://la28id.la28.org/login/index.html")
	if err != nil {
		return err
	}
	if code, _ := login["errorCode"].(float64); code != 0 {
		return fmt.Errorf("28V accounts.login failed: %v", login)
	}
	sessionInfo, _ := login["sessionInfo"].(map[string]any)
	loginToken, _ := sessionInfo["cookieValue"].(string)
	cookieName, _ := sessionInfo["cookieName"].(string)
	if cookieName == "" {
		cookieName = "gac_" + gigyaAPIKey
	}
	if loginToken == "" {
		return fmt.Errorf("28V accounts.login returned empty login_token")
	}
	for _, pair := range [][2]string{
		{cookieName, loginToken},
		{"gmid", gcid},
		{"ucid", ucid},
		{"gig_gmid", gcid},
		{"gig_ucid", ucid},
	} {
		if pair[1] == "" {
			continue
		}
		for _, domain := range []string{".gigya.com", "la28id.la28.org", ".la28.org"} {
			c.setCookie(pair[0], pair[1], domain)
		}
	}

	continueURL := gigyaFIDM + "/authorize/continue?" + url.Values{
		"context":     {context},
		"login_token": {loginToken},
		"client_id":   {oidcClientID},
		"gmidTicket":  {gmidTicket},
		"DeviceId":    {gcid},
	}.Encode()
	cont, err := c.get(continueURL, c.navHeaders("https://la28id.la28.org/proxy.html"), false)
	if err != nil {
		return err
	}
	location := headerGet(cont.Header, "Location")
	if location == "" || cont.StatusCode < 301 || cont.StatusCode > 308 {
		return fmt.Errorf("28V authorize/continue failed: %d %s", cont.StatusCode, truncate(cont.Text, 300))
	}
	if strings.HasPrefix(location, "/") {
		location = "https://fidm.eu1.gigya.com" + location
	}
	callback, err := c.get(location, c.navHeaders(continueURL), true)
	if err != nil {
		return err
	}
	fmt.Printf("[login] 28V oauth callback: %s\n", truncate(callback.URL, 200))
	cbq := mustParseURL(callback.URL).Query()
	code := cbq.Get("code")
	if code == "" {
		return fmt.Errorf("28V oauth code missing: %s", truncate(callback.URL, 200))
	}
	return c.complete28VSSO(code, cbq.Get("iss"), esID, target, xsrf, callback.URL)
}

func rewriteAuthAffiliate(authURL, affiliate string) string {
	u, err := url.Parse(authURL)
	if err != nil || affiliate == "" {
		return authURL
	}
	q := u.Query()
	changed := false
	if ru := q.Get("redirect_uri"); ru != "" && strings.Contains(ru, "affiliate=") {
		q.Set("redirect_uri", strings.ReplaceAll(ru, "affiliate=28A", "affiliate="+affiliate))
		changed = true
	}
	if st := q.Get("state"); st != "" && strings.Contains(st, "affiliate=") {
		q.Set("state", strings.ReplaceAll(st, "affiliate=28A", "affiliate="+affiliate))
		changed = true
	}
	if !changed {
		return authURL
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) complete28VSSO(code, iss, esID, target, xsrf, refererURL string) error {
	landingURL := ""
	if ref := mustParseURL(refererURL); ref.Host == "tickets.la28.org" && ref.Query().Get("code") != "" {
		q := ref.Query()
		if q.Get("affiliate") == "" {
			q.Set("affiliate", shopAffiliate)
		}
		if q.Get("esId") == "" {
			q.Set("esId", esID)
		}
		ref.RawQuery = q.Encode()
		if ref.Fragment == "" {
			ref.Fragment = "generic/,oauth"
		}
		landingURL = ref.String()
	}
	if landingURL == "" {
		stateParts := mustParseURL(target)
		stateQ := stateParts.Query()
		stateQ.Set("affiliate", shopAffiliate)
		stateQ.Set("code", code)
		if iss != "" {
			stateQ.Set("iss", iss)
		}
		if v := stateQ.Get("esId"); v == "" {
			stateQ.Set("esId", esID)
		}
		landingURL = (&url.URL{
			Scheme:   "https",
			Host:     "tickets.la28.org",
			Path:     "/mycustomerdata/",
			RawQuery: stateQ.Encode(),
			Fragment: "generic/,oauth",
		}).String()
	}
	fmt.Printf("[login] 28V SSO landing %s\n", truncate(landingURL, 160))

	shopPage, err := c.get(landingURL, c.navHeaders(refererURL), true)
	if err != nil {
		return err
	}
	xsrf2, _ := c.extractXSRF(shopPage.Text)
	if xsrf2 == "" {
		xsrf2 = xsrf
	}
	referer := strings.Split(shopPage.URL, "#")[0]
	_ = c.prepareShopAkamai(referer, shopPage.Text)

	ssoParams := url.Values{
		"code":          {code},
		"contenttype":   {"json"},
		"force_session": {"true"},
		"affiliate":     {shopAffiliate},
	}
	ssoURL := fmt.Sprintf("%s/api/singleSignOn/%s?%s", baseURL, url.PathEscape(esID), ssoParams.Encode())
	ssoResp, err := c.get(ssoURL, c.ssoGetHeaders(referer, xsrf2), true)
	if err != nil {
		return err
	}
	fmt.Printf("[login] 28V singleSignOn %d %s\n", ssoResp.StatusCode, truncate(ssoResp.Text, 220))
	for _, sc := range ssoResp.Header.Values("Set-Cookie") {
		if strings.Contains(sc, shopWebidCookie()) || strings.Contains(sc, shopWebshopCookie()) {
			fmt.Printf("[login] 28V Set-Cookie: %s\n", truncate(sc, 120))
		}
	}
	if ssoResp.StatusCode == 403 {
		_ = c.postSensors(referer, false, 1)
		ssoResp, err = c.get(ssoURL, c.ssoGetHeaders(referer, xsrf2), true)
		if err != nil {
			return err
		}
		fmt.Printf("[login] 28V singleSignOn retry %d %s\n", ssoResp.StatusCode, truncate(ssoResp.Text, 220))
	}
	if c.webshopField(shopAffiliate, "login") == "1" && !strings.Contains(c.webidValue(shopAffiliate), "NO_SESSION") {
		fmt.Printf("[login] 28V session ready webid=%s\n", truncate(c.webidValue(shopAffiliate), 40))
		return nil
	}
	return fmt.Errorf("28V still login=%s webid=%s after OIDC bootstrap", c.webshopField(shopAffiliate, "login"), truncate(c.webidValue(shopAffiliate), 40))
}

func (c *Client) webshopField(affiliate, key string) string {
	raw := c.cookieValueByName(affiliate + ".webshop")
	if raw == "" {
		return ""
	}
	decoded, err := decodeWebshop(raw)
	if err != nil {
		decoded = raw
	}
	vals, _ := url.ParseQuery(decoded)
	return vals.Get(key)
}

func (c *Client) webidValue(affiliate string) string {
	raw := c.cookieValueByName(affiliate + ".webid")
	if raw == "" {
		return ""
	}
	if d, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return string(d)
	}
	return raw
}

func (c *Client) cookieValueByName(name string) string {
	if jar, ok := c.http.GetCookieJar().(interface {
		GetAllCookies() map[string][]*http.Cookie
	}); ok {
		for _, cookies := range jar.GetAllCookies() {
			for _, ck := range cookies {
				if ck.Name == name {
					return ck.Value
				}
			}
		}
	}
	for _, raw := range []string{baseURL + "/", "https://la28.org/", "https://tickets.la28.org/"} {
		for _, ck := range c.http.GetCookies(mustParseURL(raw)) {
			if ck.Name == name {
				return ck.Value
			}
		}
	}
	return ""
}

func (c *Client) logWebshopLoginState() {
	for _, a := range []string{"28A", shopAffiliate} {
		fmt.Printf("[login] %s.webshop login=%s webid=%s\n",
			a, c.webshopField(a, "login"), truncate(c.webidValue(a), 40))
	}
}

func (c *Client) Run(email, password string, maxPolls int) (*RunResult, error) {
	return c.RunWithBasket(email, password, maxPolls, nil, 0)
}

func (c *Client) RunWithBasket(email, password string, maxPolls int, tickets []filteredTicket, maxQty int) (*RunResult, error) {
	queue, err := c.JoinAndWait(maxPolls)
	if err != nil {
		return nil, err
	}
	result := &RunResult{Queue: queue}
	if queue.RedirectURL == "" && maxPolls > 0 {
		return result, nil
	}
	if email != "" && password != "" {
		login, err := c.Login(email, password)
		if err != nil {
			return result, err
		}
		result.Login = login
	}
	if len(tickets) > 0 {
		basket, err := c.AddFilteredToBasket(tickets, maxQty)
		if err != nil {
			return result, err
		}
		result.Basket = basket
	}
	return result, nil
}
