package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
	http "github.com/bogdanfinn/fhttp"
)

func (c *Client) hasSecCptSolved() bool {
	return strings.Contains(c.cookie("sec_cpt"), "~3~")
}

// solveSecCptIfPresent handles Akamai SEC-CPT crypto challenges (HTML iframe or JSON 428).
func (c *Client) solveSecCptIfPresent(pageURL, body string) (bool, error) {
	trimmed := strings.TrimSpace(body)
	hasHTML := strings.Contains(body, "sec-cpt-if") || strings.Contains(body, `challenge="`)
	hasJSON := strings.Contains(body, `"sec-cp-challenge"`) ||
		(strings.HasPrefix(trimmed, "{") && strings.Contains(body, `"provider"`))
	if !hasHTML && !hasJSON {
		return false, nil
	}

	var (
		ch  *akamai.SecCptChallenge
		err error
	)
	if hasJSON && !hasHTML {
		ch, err = akamai.ParseSecCptChallengeFromJson(strings.NewReader(body))
	} else {
		ch, err = akamai.ParseSecCptChallenge(strings.NewReader(body))
		if err != nil && hasJSON {
			ch, err = akamai.ParseSecCptChallengeFromJson(strings.NewReader(body))
		}
	}
	if err != nil {
		return false, fmt.Errorf("parse sec-cpt: %w", err)
	}

	secCookie := c.cookie("sec_cpt")
	if secCookie == "" {
		return false, fmt.Errorf("sec_cpt cookie missing before challenge solve")
	}
	payload, err := ch.GenerateSecCptPayload(secCookie)
	if err != nil {
		return false, fmt.Errorf("sec-cpt payload: %w", err)
	}

	fmt.Printf("[sec-cpt] challenge path=%s — waiting mandatory duration\n", truncate(ch.ChallengePath, 100))
	ch.Sleep()

	headers := http.Header{
		"sec-ch-ua":          {secChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {secChUAPlatform},
		"user-agent":         {userAgent},
		"content-type":       {"application/json"},
		"accept":             {"*/*"},
		"origin":             {baseURL},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {pageURL},
		"accept-encoding":    {"gzip, deflate, br, zstd"},
		"accept-language":    {"en-US,en;q=0.9"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"user-agent", "content-type", "accept", "origin",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language",
		},
	}

	// Hyper docs: POST PoW to /_sec/verify?provider=crypto; also try challenge path.
	postTargets := []string{baseURL + "/_sec/verify?provider=crypto"}
	if ch.ChallengePath != "" {
		p := ch.ChallengePath
		if strings.HasPrefix(p, "/") {
			u := mustParseURL(pageURL)
			p = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, p)
		}
		postTargets = append(postTargets, p)
	}
	for _, postURL := range postTargets {
		resp, err := c.post(postURL, headers, bytes.NewReader(payload), true)
		if err != nil {
			fmt.Printf("[sec-cpt] POST %s err: %v\n", truncate(postURL, 60), err)
			continue
		}
		fmt.Printf("[sec-cpt] POST %s → %d (%d bytes)\n", truncate(postURL, 60), resp.StatusCode, len(resp.Body))
		if c.hasSecCptSolved() {
			break
		}
	}

	v, err := c.get(baseURL+"/_sec/cp_challenge/verify", http.Header{
		"sec-ch-ua":          {secChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {secChUAPlatform},
		"user-agent":         {userAgent},
		"accept":             {"*/*"},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-dest":     {"empty"},
		"referer":            {pageURL},
		"accept-encoding":    {"gzip, deflate, br, zstd"},
		"accept-language":    {"en-US,en;q=0.9"},
	}, true)
	if err != nil {
		return false, err
	}
	fmt.Printf("[sec-cpt] cp_challenge/verify → %d %s\n", v.StatusCode, truncate(v.Text, 80))
	fmt.Printf("[sec-cpt] solved=%v\n", c.hasSecCptSolved())
	return true, nil
}
