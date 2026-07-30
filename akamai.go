package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
	"github.com/Hyper-Solutions/hyper-sdk-go/v2/akamai"
)

func (c *Client) solveAkamaiForURL(pageURL, html string, fresh bool) error {
	path, err := akamai.ParseScriptPath(strings.NewReader(html))
	if err != nil {
		if c.sensorEndpoint != "" {
			fmt.Println("[akamai] reusing previous sensor endpoint")
		} else {
			fmt.Println("[akamai] no sensor script found on page")
			return nil
		}
	} else {
		u := mustParseURL(pageURL)
		c.sensorEndpoint = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, path)

		headers := http.Header{
			"sec-ch-ua":          {secChUA},
			"sec-ch-ua-mobile":   {"?0"},
			"user-agent":         {userAgent},
			"sec-ch-ua-platform": {secChUAPlatform},
			"accept":             {"*/*"},
			"sec-fetch-site":     {"same-origin"},
			"sec-fetch-mode":     {"no-cors"},
			"sec-fetch-dest":     {"script"},
			"referer":            {pageURL},
			"accept-encoding":    {"gzip, deflate, br, zstd"},
			"accept-language":    {"en-US,en;q=0.9"},
			"priority":           {"u=1"},
			http.HeaderOrderKey: {
				"sec-ch-ua", "sec-ch-ua-mobile", "user-agent", "sec-ch-ua-platform",
				"accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest",
				"referer", "accept-encoding", "accept-language", "priority",
			},
		}
		resp, err := c.get(c.sensorEndpoint, headers, true)
		if err != nil {
			return err
		}
		c.sensorScript = resp.Text
		fmt.Printf("[akamai] fetched sensor script (%d bytes)\n", len(c.sensorScript))
	}
	return c.postSensors(pageURL, fresh, maxSensorPosts)
}

func (c *Client) postSensors(pageURL string, fresh bool, maxPosts int) error {
	if c.sensorEndpoint == "" {
		return fmt.Errorf("sensor endpoint not set; call solveAkamaiForURL first")
	}
	if maxPosts <= 0 {
		maxPosts = maxSensorPosts
	}
	if fresh {
		c.sensorContext = ""
	}

	u := mustParseURL(pageURL)
	origin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	useScript := fresh || c.sensorContext == ""

	for i := 0; i < maxPosts; i++ {
		script := ""
		if useScript && i == 0 {
			script = c.sensorScript
		}
		sensorData, ctx, err := c.hyper.GenerateSensorData(context.Background(), &hyper.SensorInput{
			Abck:           c.cookie("_abck"),
			Bmsz:           c.cookie("bm_sz"),
			Version:        "3",
			PageUrl:        pageURL,
			UserAgent:      userAgent,
			ScriptUrl:      c.sensorEndpoint,
			AcceptLanguage: "en-US,en;q=0.9",
			IP:             c.ip,
			Context:        c.sensorContext,
			Script:         script,
		})
		if err != nil {
			return fmt.Errorf("generate sensor: %w", err)
		}
		c.sensorContext = ctx

		payload, _ := json.Marshal(map[string]string{"sensor_data": sensorData})
		headers := http.Header{
			"sec-ch-ua":          {secChUA},
			"sec-ch-ua-platform": {secChUAPlatform},
			"sec-ch-ua-mobile":   {"?0"},
			"user-agent":         {userAgent},
			"content-type":       {"text/plain;charset=UTF-8"},
			"accept":             {"*/*"},
			"origin":             {origin},
			"sec-fetch-site":     {"same-origin"},
			"sec-fetch-mode":     {"cors"},
			"sec-fetch-dest":     {"empty"},
			"referer":            {pageURL},
			"accept-encoding":    {"gzip, deflate, br, zstd"},
			"accept-language":    {"en-US,en;q=0.9"},
			"priority":           {"u=1, i"},
			http.HeaderOrderKey: {
				"sec-ch-ua", "sec-ch-ua-platform", "sec-ch-ua-mobile", "user-agent",
				"content-type", "accept", "origin", "sec-fetch-site", "sec-fetch-mode",
				"sec-fetch-dest", "referer", "accept-encoding", "accept-language", "priority",
			},
		}
		if _, err := c.post(c.sensorEndpoint, headers, bytes.NewReader(payload), true); err != nil {
			return err
		}
		abck := c.cookie("_abck")
		tail := abck
		if len(tail) > 24 {
			tail = tail[len(tail)-24:]
		}
		fmt.Printf("[akamai] sensor post %d/%d, abck tail: ...%s\n", i+1, maxPosts, tail)

		// Stop as soon as Akamai signals the cookie is trusted.
		if strings.Contains(abck, "~0~") || akamai.IsCookieValid(abck, i) {
			fmt.Println("[akamai] cookie valid")
			return nil
		}
	}

	abck := c.cookie("_abck")
	if strings.Contains(abck, "~0~") || akamai.IsCookieValid(abck, maxPosts-1) {
		fmt.Println("[akamai] cookie valid")
	} else if akamai.IsCookieInvalidated(abck) {
		fmt.Println("[akamai] cookie still invalidated after sensor posts")
	} else {
		fmt.Println("[akamai] finished sensor posts (continuing)")
	}
	return nil
}

func (c *Client) refreshAkamai(pageURL string) error {
	// After validator (protected call), Hyper docs: usually 1 sensor is enough.
	if c.sensorEndpoint == "" {
		resp, err := c.get(pageURL, c.navHeaders(""), true)
		if err != nil {
			return err
		}
		return c.solveAkamaiForURL(resp.URL, resp.Text, true)
	}
	fmt.Println("[akamai] refreshing cookie (1 sensor)")
	return c.postSensors(pageURL, false, 1)
}

// prepareShopAkamai refreshes Bot Manager cookies on the shop origin after SSO redirects.
func (c *Client) prepareShopAkamai(pageURL, html string) error {
	path, err := akamai.ParseScriptPath(strings.NewReader(html))
	if err == nil {
		u := mustParseURL(pageURL)
		endpoint := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, path)
		if endpoint != c.sensorEndpoint {
			c.sensorEndpoint = endpoint
			headers := http.Header{
				"sec-ch-ua":          {secChUA},
				"sec-ch-ua-mobile":   {"?0"},
				"user-agent":         {userAgent},
				"sec-ch-ua-platform": {secChUAPlatform},
				"accept":             {"*/*"},
				"sec-fetch-site":     {"same-origin"},
				"sec-fetch-mode":     {"no-cors"},
				"sec-fetch-dest":     {"script"},
				"referer":            {pageURL},
				"accept-encoding":    {"gzip, deflate, br, zstd"},
				"accept-language":    {"en-US,en;q=0.9"},
				"priority":           {"u=1"},
			}
			resp, err := c.get(c.sensorEndpoint, headers, true)
			if err != nil {
				return err
			}
			c.sensorScript = resp.Text
			c.sensorContext = ""
			fmt.Printf("[akamai] shop sensor script (%d bytes)\n", len(c.sensorScript))
			// First post after script change needs the script payload.
			return c.postSensors(pageURL, true, 1)
		}
	}
	return c.postSensors(pageURL, false, 1)
}
