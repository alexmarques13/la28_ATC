package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	http "github.com/bogdanfinn/fhttp"
)

func (c *Client) extractXSRF(html string) (string, error) {
	m := reXSRF.FindStringSubmatch(html)
	if len(m) < 2 {
		return "", fmt.Errorf("xsrfToken not found on page")
	}
	return m[1], nil
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
		"user-agent":       {userAgent},
		"content-type":     {"application/x-www-form-urlencoded"},
		"accept":           {"*/*"},
		"origin":           {"https://la28id.la28.org"},
		"referer":          {referer},
		"accept-language":  {"en-US,en;q=0.9"},
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
	if stateQ.Get("affiliate") == "" {
		// Keep configured affiliate (e.g. 28V) when SSO state omits it.
		if tq := mustParseURL(c.TargetURL).Query(); tq.Get("affiliate") != "" {
			stateQ.Set("affiliate", tq.Get("affiliate"))
		} else if cbq.Get("affiliate") != "" {
			stateQ.Set("affiliate", cbq.Get("affiliate"))
		}
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

	// OAuth round-trip leaves shop _abck stale; Akamai 403s singleSignOn without a refresh.
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

	// One cheap retry if Akamai blocked the first attempt.
	if ssoResp.StatusCode == 403 {
		fmt.Println("[login] singleSignOn 403 — refreshing akamai and retrying")
		_ = c.postSensors(referer, false, 1)
		// refresh xsrf after sensor posts
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

	verify, err := c.get(c.TargetURL, c.navHeaders(c.TargetURL), true)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[login] done logged_in=True url=%s\n", verify.URL)

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

func (c *Client) Run(email, password string, maxPolls int) (*RunResult, error) {
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
	return result, nil
}
