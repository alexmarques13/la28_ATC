package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

func twocaptchaPost(path string, payload map[string]any, retries int) (map[string]any, error) {
	if retries <= 0 {
		retries = 5
	}
	url := "https://api.2captcha.com/" + path
	p := fp()
	jar := tls_client.NewCookieJar()
	client, err := tls_client.NewHttpClient(
		tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(clientProfileFor(p.Impersonate)),
		tls_client.WithCookieJar(jar),
	)
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(raw))
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", p.UA)
		req.Header[http.HeaderOrderKey] = []string{"content-type", "user-agent"}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("2captcha HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			preview := string(body)
			if len(preview) > 200 {
				preview = preview[:200]
			}
			return nil, fmt.Errorf("2captcha HTTP %d: %s", resp.StatusCode, preview)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			lastErr = err
			time.Sleep(time.Duration(min(2*attempt, 8)) * time.Second)
			continue
		}
		return out, nil
	}
	return nil, fmt.Errorf("2captcha %s failed after retries: %v", path, lastErr)
}

func twocaptchaImageText(imageB64, apiKey string) (string, error) {
	created, err := twocaptchaPost("createTask", map[string]any{
		"clientKey": apiKey,
		"task": map[string]any{
			"type":      "ImageToTextTask",
			"body":      imageB64,
			"phrase":    false,
			"case":      true,
			"numeric":   0,
			"math":      false,
			"minLength": 3,
			"maxLength": 8,
			"comment":   "Type the characters shown in the captcha image exactly.",
		},
	}, 5)
	if err != nil {
		return "", err
	}
	if eid, _ := created["errorId"].(float64); eid != 0 {
		return "", fmt.Errorf("2captcha createTask: %v", created)
	}
	taskID := created["taskId"]
	fmt.Printf("[2captcha] ImageToText taskId=%v\n", taskID)

	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		result, err := twocaptchaPost("getTaskResult", map[string]any{
			"clientKey": apiKey,
			"taskId":    taskID,
		}, 5)
		if err != nil {
			return "", err
		}
		if eid, _ := result["errorId"].(float64); eid != 0 {
			return "", fmt.Errorf("2captcha getTaskResult: %v", result)
		}
		if status, _ := result["status"].(string); status == "ready" {
			text := ""
			if sol, ok := result["solution"].(map[string]any); ok {
				text, _ = sol["text"].(string)
			}
			fmt.Printf("[2captcha] solution=%q\n", text)
			return strings.TrimSpace(text), nil
		}
	}
	return "", fmt.Errorf("2captcha ImageToText timeout")
}

func solveBotDetect(session *Session, cfg *SoftblockConfig, apiKey string, maxOCR int) (map[string]any, error) {
	if maxOCR <= 0 {
		maxOCR = 3
	}
	culture := strings.ToLower(cfg.Culture)
	if culture == "" {
		culture = "en-us"
	}
	challengeURL := fmt.Sprintf("%s%s/challengeapi/queueitcaptcha/challenge/%s", baseURL, cfg.QueuePathPrefix, culture)
	sessionID := ""
	var lastPayload any

	for ocrTry := 1; ocrTry <= maxOCR; ocrTry++ {
		fetchURL := challengeURL
		if sessionID != "" {
			fetchURL = challengeURL + "/" + sessionID
		}
		t0 := time.Now()

		hdrs := chromeHeaders(map[string]string{
			"accept":         "application/json, text/javascript, */*; q=0.01",
			"content-type":   "application/json",
			"origin":         baseURL,
			"referer":        baseURL + cfg.QueuePathPrefix + "/softblock/",
			"sec-fetch-dest": "empty",
			"sec-fetch-mode": "cors",
			"sec-fetch-site": "same-origin",
			"x-requested-with": "XMLHttpRequest",
		}, "xhr")
		for k, v := range challengeHeaders(cfg, true) {
			hdrs = append(hdrs, headerKV{k, v})
		}

		r, err := session.do("POST", fetchURL, hdrs, strings.NewReader(""), 0)
		if err != nil {
			return nil, err
		}
		fmt.Printf("[botdetect] challenge POST -> %d (ocr %d/%d)\n", r.StatusCode, ocrTry, maxOCR)
		if r.StatusCode != 200 {
			preview := r.Body
			if len(preview) > 300 {
				preview = preview[:300]
			}
			return nil, fmt.Errorf("challenge fetch failed: %d %s", r.StatusCode, preview)
		}
		var ch map[string]any
		if err := json.Unmarshal([]byte(r.Body), &ch); err != nil {
			return nil, err
		}
		if sid, ok := ch["sessionId"].(string); ok && sid != "" {
			sessionID = sid
		}
		details := ch["challengeDetails"]
		imageB64, _ := ch["imageBase64"].(string)
		if imageB64 == "" {
			keys := make([]string, 0, len(ch))
			for k := range ch {
				keys = append(keys, k)
			}
			return nil, fmt.Errorf("no imageBase64 in challenge: keys=%v", keys)
		}

		solution, err := twocaptchaImageText(imageB64, apiKey)
		if err != nil {
			return nil, err
		}
		p := fp()
		duration := int(time.Since(t0).Milliseconds())
		if duration < 1 {
			duration = 1
		}
		stats := map[string]any{
			"browser":        "Chrome",
			"browserVersion": fmt.Sprintf("%d.0.0.0", p.UAMajor),
			"cookiesEnabled": true,
			"duration":       duration,
			"isMobile":       false,
			"os":             "Windows",
			"osVersion":      "10",
			"screen":         p.Screen,
			"tries":          ocrTry,
			"userAgent":      p.UA,
		}

		verifyURL := baseURL + cfg.ChallengeVerifyEndpoint
		body := map[string]any{
			"challengeType":             "botdetect",
			"sessionId":                 sessionID,
			"challengeDetails":          details,
			"solution":                  solution,
			"stats":                     stats,
			"customerId":                cfg.CustomerID,
			"eventId":                   cfg.EventID,
			"challengesIssuedByReason":  cfg.ChallengesIssuedByReason,
			"version":                   6,
		}
		bodyJSON, _ := json.Marshal(body)

		vh := chromeHeaders(map[string]string{
			"accept":          "application/json, text/javascript, */*; q=0.01",
			"content-type":    "application/json",
			"origin":          baseURL,
			"referer":         baseURL + cfg.QueuePathPrefix + "/softblock/",
			"sec-fetch-dest":  "empty",
			"sec-fetch-mode":  "cors",
			"sec-fetch-site":  "same-origin",
			"x-requested-with": "XMLHttpRequest",
		}, "xhr")
		for k, v := range challengeHeaders(cfg, false) {
			vh = append(vh, headerKV{k, v})
		}

		vr, err := session.do("POST", verifyURL, vh, strings.NewReader(string(bodyJSON)), 0)
		if err != nil {
			return nil, err
		}
		preview := vr.Body
		if len(preview) > 240 {
			preview = preview[:240]
		}
		fmt.Printf("[botdetect] verify -> %d %s\n", vr.StatusCode, preview)
		if vr.StatusCode != 200 {
			p2 := vr.Body
			if len(p2) > 300 {
				p2 = p2[:300]
			}
			return nil, fmt.Errorf("BotDetect verify HTTP %d: %s", vr.StatusCode, p2)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(vr.Body), &data); err != nil {
			return nil, err
		}
		lastPayload = data
		if data["sessionInfo"] != nil || truthy(data["isVerified"]) || truthy(data["verified"]) {
			if si, ok := data["sessionInfo"].(map[string]any); ok {
				return si, nil
			}
			return data, nil
		}
		if _, ok := data["sessionId"]; ok {
			return data, nil
		}
		if _, ok := data["challengeSessionId"]; ok {
			return data, nil
		}
		errs, _ := data["errorCodes"].([]any)
		wrong := false
		for _, e := range errs {
			if s, ok := e.(string); ok && s == "WrongUserInput" {
				wrong = true
				break
			}
		}
		if wrong && ocrTry < maxOCR {
			fmt.Println("[botdetect] WrongUserInput — new image (same Softblock visit)")
			continue
		}
		break
	}
	return nil, fmt.Errorf("BotDetect verify failed: %v", lastPayload)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	default:
		return v != nil
	}
}
