package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
	http "github.com/bogdanfinn/fhttp"
)

var reSbsdScript = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+\?v=([0-9a-fA-F-]{8,})[^"']*)["']`)

func isSbsdChallenge(html string) bool {
	return strings.Contains(html, "sec-if-cpt-container") ||
		(reSbsdScript.MatchString(html) && len(html) < 20000)
}

func (c *Client) solveSbsdIfPresent(pageURL, html string) (bool, error) {
	if !isSbsdChallenge(html) {
		return false, nil
	}
	m := reSbsdScript.FindStringSubmatch(html)
	if len(m) < 3 {
		return false, fmt.Errorf("sbsd challenge detected but script src not found")
	}
	scriptSrc := m[1]
	uuid := m[2]
	if strings.HasPrefix(scriptSrc, "/") {
		u := mustParseURL(pageURL)
		scriptSrc = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, scriptSrc)
	}
	scriptURL, err := url.Parse(scriptSrc)
	if err != nil {
		return false, err
	}
	t := scriptURL.Query().Get("t")
	postURL := scriptURL.Scheme + "://" + scriptURL.Host + scriptURL.Path
	if t != "" {
		postURL += "?t=" + url.QueryEscape(t)
	}

	fmt.Printf("[sbsd] challenge detected uuid=%s\n", uuid)
	scriptResp, err := c.get(scriptSrc, http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"user-agent":         {c.fp.UserAgent},
		"sec-ch-ua-platform": {c.fp.Platform},
		"accept":             {"*/*"},
		"sec-fetch-site":     {"same-origin"},
		"sec-fetch-mode":     {"no-cors"},
		"sec-fetch-dest":     {"script"},
		"referer":            {pageURL},
		"accept-encoding":    {"gzip, deflate, br, zstd"},
		"accept-language":    {"en-US,en;q=0.9"},
		http.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "user-agent", "sec-ch-ua-platform",
			"accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
			"referer", "accept-encoding", "accept-language",
		},
	}, true)
	if err != nil {
		return false, err
	}
	fmt.Printf("[sbsd] fetched script (%d bytes)\n", len(scriptResp.Body))

	oCookie := c.cookie("sbsd_o")
	if oCookie == "" {
		oCookie = c.cookie("bm_so")
	}

	for index := 0; index < 2; index++ {
		if v := c.cookie("sbsd_o"); v != "" {
			oCookie = v
		} else if v := c.cookie("bm_so"); v != "" {
			oCookie = v
		}
		payload, err := c.hyper.GenerateSbsdData(context.Background(), &hyper.SbsdInput{
			Index:          index,
			UserAgent:      c.fp.UserAgent,
			Uuid:           uuid,
			PageUrl:        pageURL,
			OCookie:        oCookie,
			Script:         scriptResp.Text,
			AcceptLanguage: "en-US,en;q=0.9",
			IP:             c.ip,
		})
		if err != nil {
			return false, fmt.Errorf("sbsd generate index %d: %w", index, err)
		}
		body, _ := json.Marshal(map[string]string{"body": payload})
		headers := http.Header{
			"sec-ch-ua":          {c.fp.SecChUA},
			"sec-ch-ua-mobile":   {"?0"},
			"sec-ch-ua-platform": {c.fp.Platform},
			"user-agent":         {c.fp.UserAgent},
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
		resp, err := c.post(postURL, headers, bytes.NewReader(body), true)
		if err != nil {
			return false, fmt.Errorf("sbsd post index %d: %w", index, err)
		}
		fmt.Printf("[sbsd] post index %d → %d (%d bytes)\n", index, resp.StatusCode, len(resp.Body))
	}
	if c.sensorEndpoint != "" {
		_ = c.postSensors(pageURL, false, maxSensorPosts)
	}
	return true, nil
}

func (c *Client) getProtected(pageURL, referer string) (*httpResult, error) {
	if err := c.ensureQueueAccess(pageURL); err != nil {
		return nil, err
	}

	var last *httpResult
	for attempt := 0; attempt < 4; attempt++ {
		r, err := c.get(pageURL, c.navHeaders(referer), false)
		if err != nil {
			return nil, err
		}
		if loc := headerGet(r.Header, "Location"); loc != "" && r.StatusCode >= 300 && r.StatusCode < 400 {
			if !strings.HasPrefix(loc, "http") {
				base := mustParseURL(pageURL)
				loc = base.ResolveReference(mustParseURL(loc)).String()
			}
			if isQueueitURL(loc) || hasQueueitToken(loc) {
				fmt.Printf("[queue] page still gated (%s) — redeem/join once\n", truncate(loc, 100))
				if qErr := c.ensureQueueAccess(pageURL); qErr != nil {
					return nil, qErr
				}
				continue
			}
			r, err = c.get(loc, c.navHeaders(pageURL), false)
			if err != nil {
				return nil, err
			}
		}
		last = r
		if isQueueitURL(r.URL) || hasQueueitToken(r.URL) {
			fmt.Printf("[queue] page still gated (%s) — redeem/join once\n", truncate(r.URL, 100))
			if _, err := c.JoinAndWaitForURL(pageURL, 0); err != nil {
				return nil, err
			}
			continue
		}
		solved, err := c.solveSbsdIfPresent(r.URL, r.Text)
		if err != nil {
			return nil, err
		}
		if solved {
			fmt.Println("[sbsd] retrying page after challenge")
			continue
		}
		if _, err := c.solvePixelIfPresent(r.URL, r.Text); err != nil {
			fmt.Printf("[pixel] warning: %v\n", err)
		}
		if solved, err := c.solveSecCptIfPresent(r.URL, r.Text); err != nil {
			fmt.Printf("[sec-cpt] warning: %v\n", err)
		} else if solved {
			fmt.Println("[sec-cpt] retrying page after challenge")
			continue
		}
		if err := c.prepareShopAkamai(r.URL, r.Text); err != nil {
			fmt.Printf("[akamai] prepare warning: %v\n", err)
		}
		if isSbsdChallenge(r.Text) {
			continue
		}
		return r, nil
	}
	return last, fmt.Errorf("still blocked by akamai challenge after retries (len=%d)", len(last.Body))
}

func (c *Client) ensureQueueAccess(targetURL string) error {
	probe, err := c.get(targetURL, c.navHeaders(""), false)
	if err != nil {
		return err
	}
	loc := headerGet(probe.Header, "Location")
	if loc != "" && !strings.HasPrefix(loc, "http") {
		base := mustParseURL(targetURL)
		loc = base.ResolveReference(mustParseURL(loc)).String()
	}
	needsQueue := isQueueitURL(probe.URL) || isQueueitURL(loc) || hasQueueitToken(loc) ||
		(probe.StatusCode >= 300 && probe.StatusCode < 400 && strings.Contains(strings.ToLower(loc), "queue"))
	if !needsQueue {
		return nil
	}

	ev := queueEventIDFromURL(loc)
	if ev == "" {
		ev = queueEventIDFromURL(probe.URL)
	}
	if c.hasQueueAcceptedCookie(ev) {
		fmt.Printf("[queue] already passed %s — reusing QueueITAccepted cookie\n", ev)
		return c.redeemQueueitAccess(targetURL, loc)
	}

	fmt.Printf("[queue] %s is behind Queue-it — joining\n", truncate(targetURL, 80))
	_, err = c.JoinAndWaitForURL(targetURL, 0)
	return err
}
