package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
	http "github.com/bogdanfinn/fhttp"
)

// solvePixelIfPresent solves Akamai's pixel challenge when present on a page.
// Missing this step commonly causes Access Denied on protected POSTs (checkout/ATC).
func (c *Client) solvePixelIfPresent(pageURL, html string) (bool, error) {
	htmlVar, err := akamai.ParsePixelHtmlVar(strings.NewReader(html))
	if err != nil {
		return false, nil // not present
	}
	scriptURL, postURL, err := akamai.ParsePixelScriptURL(strings.NewReader(html))
	if err != nil {
		return false, fmt.Errorf("pixel script url: %w", err)
	}
	if strings.HasPrefix(scriptURL, "/") {
		u := mustParseURL(pageURL)
		scriptURL = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, scriptURL)
	}
	if strings.HasPrefix(postURL, "/") {
		u := mustParseURL(pageURL)
		postURL = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, postURL)
	}

	fmt.Printf("[pixel] challenge htmlVar=%d script=%s\n", htmlVar, truncate(scriptURL, 80))
	scriptResp, err := c.get(scriptURL, http.Header{
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
	scriptVar, err := akamai.ParsePixelScriptVar(strings.NewReader(scriptResp.Text))
	if err != nil {
		return false, fmt.Errorf("pixel script var: %w", err)
	}

	payload, err := c.hyper.GeneratePixelData(context.Background(), &hyper.PixelInput{
		UserAgent:      c.fp.UserAgent,
		HTMLVar:        fmt.Sprintf("%d", htmlVar),
		ScriptVar:      scriptVar,
		AcceptLanguage: "en-US,en;q=0.9",
		IP:             c.ip,
	})
	if err != nil {
		return false, fmt.Errorf("pixel generate: %w", err)
	}

	headers := http.Header{
		"sec-ch-ua":          {c.fp.SecChUA},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {c.fp.Platform},
		"user-agent":         {c.fp.UserAgent},
		"content-type":       {"application/x-www-form-urlencoded"},
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
	resp, err := c.post(postURL, headers, bytes.NewReader([]byte(payload)), true)
	if err != nil {
		return false, err
	}
	fmt.Printf("[pixel] post → %d (%d bytes)\n", resp.StatusCode, len(resp.Body))
	return true, nil
}
