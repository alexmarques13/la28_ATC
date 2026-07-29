package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Hyper-Solutions/hyper-sdk-go/v2"
)

func runOnce(target, twocaptchaKey string, hyperSession *hyper.Session) (int, error) {
	profile := randomFingerprint()
	setFingerprint(profile)
	session, err := makeSession()
	if err != nil {
		return 1, err
	}
	ip, err := publicIP(session)
	if err != nil {
		return 1, err
	}
	fmt.Printf("[net] ip=%s impersonate=%s ua=Chrome/%d lang=%q screen=%q\n",
		ip, profile.Impersonate, profile.UAMajor, profile.AcceptLang, profile.Screen)
	fmt.Printf("[fp] sec-ch-ua=%s\n", profile.SecChUA)
	keyPrev := profile.Key
	if len(keyPrev) > 120 {
		keyPrev = keyPrev[:120]
	}
	fmt.Printf("[fp] key=%s...\n", keyPrev)

	finalURL, html, err := enrichAndEnterQueue(session, hyperSession, ip, target)
	if err != nil {
		return 1, err
	}
	fmt.Printf("[entry] %s\n", finalURL)

	if !strings.Contains(finalURL, "softblock") && !strings.Contains(html, "SoftblockViewModel") {
		if strings.Contains(finalURL, "queueittoken") ||
			strings.Contains(html, "InQueueViewModel") ||
			strings.Contains(finalURL, "/queue/") {
			fmt.Println("[entry] reached queue/target without softblock")
			return 0, nil
		}
		fmt.Println("[entry] no softblock on direct path")
		return 1, nil
	}

	cfg, err := parseSoftblockConfig(html)
	if err != nil {
		return 1, err
	}
	names := challengeNames(cfg.Challenges)
	tok := extractEnqueueToken(cfg.inqueueURL())
	if tok == "" {
		tok = extractEnqueueToken(finalURL)
	}
	fmt.Printf("[softblock] c=%s e=%s challenges=%v rule=%q reason=%d token_ip=%v\n",
		cfg.CustomerID, cfg.EventID, names, cfg.RuleName, cfg.ChallengesIssuedByReason, enqueueTokenIP(tok))

	if strings.Contains(cfg.RuleName, "Hard Block") {
		fmt.Println("[fp] Softblock Hard Block — skip captcha")
		return 4, nil
	}
	if strings.Contains(cfg.RuleName, "Bot Deflector") {
		fmt.Println("[fp] Softblock Bot Deflector — proceed")
	}

	softblockRef := finalURL
	if !strings.Contains(finalURL, "softblock") {
		softblockRef = fmt.Sprintf(
			"%s%s/softblock/?c=%s&e=%s&t=%s&rticr=2",
			baseURL, cfg.QueuePathPrefix,
			quoteLikePython(cfg.CustomerID),
			quoteLikePython(cfg.EventID),
			quoteLikePython(orDefault(cfg.TargetURL, target)),
		)
	}

	cfg, sessionInfo, inqueueHTML, viewRef, enqueueToken, softblockRef, err := clearSoftblockOnce(
		session, cfg, hyperSession, ip, twocaptchaKey, target, softblockRef, html,
	)
	if err != nil {
		fmt.Printf("[fatal] Softblock clear failed: %v\n", err)
		return 3, nil
	}

	var scv string
	if sessionInfo != nil {
		b, _ := json.Marshal(sessionInfo)
		scv = string(b)
	}
	inqOpts := parseInqueueOptions(inqueueHTML)
	if layout, ok := inqOpts["layout"].(string); ok && layout != "" {
		cfg.Layout = layout
	}
	if hash, ok := inqOpts["challengeApiChecksumHash"].(string); ok && hash != "" {
		cfg.ChallengeAPIChecksumHash = hash
	}
	bmReason := 1
	if r, ok := inqOpts["challengesIssuedByReason"].(int); ok {
		bmReason = r
	} else if r, ok := inqOpts["challengesIssuedByReason"].(float64); ok {
		bmReason = int(r)
	}
	var challengeNamesList []string
	if chs, ok := inqOpts["challenges"].([]map[string]any); ok {
		challengeNamesList = challengeNames(chs)
	} else if chs, ok := inqOpts["challenges"].([]any); ok {
		for _, c := range chs {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					challengeNamesList = append(challengeNamesList, n)
				}
			}
		}
	}
	fmt.Printf("[inqueue] challenges=%v showAkamai=%v ttl_ms=%v\n",
		challengeNamesList, inqOpts["showAkamaiBotManagerHeaderVerificationChallenge"],
		ttlString(enqueueTokenTTLMs(enqueueToken)))

	fmt.Println("[akamai] Hyper on /queue/view before BM/enqueue")
	htmlPtr := &inqueueHTML
	_, _ = postHyperSensorOnce(session, hyperSession, viewRef, ip, htmlPtr)

	var queueitEnqueueToken string
	needsBM := containsStr(challengeNamesList, "AkamaiBotManagerHeaderVerification")
	if show, ok := inqOpts["showAkamaiBotManagerHeaderVerificationChallenge"].(bool); ok && show {
		needsBM = true
	}
	if needsBM {
		queueitEnqueueToken, err = solveAkamaiBMHeader(session, cfg, bmReason, viewRef)
		if err != nil {
			return 1, err
		}
	}
	fmt.Printf("[token] pre-enqueue ttl_ms=%v\n", ttlString(enqueueTokenTTLMs(enqueueToken)))

	enq, err := enqueue(session, cfg, enqueueToken, scv, queueitEnqueueToken, viewRef, softblockRef, []any{})
	if err != nil {
		return 1, err
	}

	if restricted, _ := enq["restricted"].(bool); restricted {
		fmt.Printf("[enqueue] Restricted (Access Rule) (softblock_rule=%q)\n", cfg.RuleName)
		return 4, nil
	}

	queueID, _ := enq["queueId"].(string)
	if challengeRequired, _ := enq["challengeRequired"].(bool); challengeRequired {
		if redir, ok := enq["redirectUrl"].(string); ok {
			fmt.Println("[enqueue] challengeRequired ->", redir)
		}
		return 3, nil
	}
	if redir, ok := enq["redirectUrl"].(string); ok && redir != "" && queueID == "" {
		fmt.Println("[enqueue] redirect", redir)
		return 0, nil
	}
	if queueID == "" || strings.HasPrefix(queueID, "00000000") {
		return 1, fmt.Errorf("no queueId after enqueue: %v", enq)
	}

	fmt.Printf("[queue] queueId=%s\n", queueID)
	result, err := pollStatus(session, cfg, queueID, enqueueToken, 2.0, 3600.0)
	if err != nil {
		return 1, err
	}
	var landingURL, landingHTML string
	if redir, ok := result["redirectUrl"].(string); ok && redir != "" {
		rr, err := session.do("GET", redir, chromeHeaders(map[string]string{
			"referer": baseURL + cfg.QueuePathPrefix + "/",
		}, "navigate"), nil, 0)
		if err != nil {
			return 1, err
		}
		fmt.Printf("[done] %d %s\n", rr.StatusCode, rr.URL)
		landingURL, landingHTML = rr.URL, rr.Body
	}

	if landingURL == "" {
		landingURL = target
	}
	if err := completeLogin(session, landingURL, landingHTML, twocaptchaKey); err != nil {
		fmt.Printf("[login] failed: %v\n", err)
		return 1, err
	}
	return 0, nil
}

func run(target string) (int, error) {
	twocaptchaKey, err := requireEnv("TWOCAPTCHA_API_KEY")
	if err != nil {
		return 1, err
	}
	hyperKey, err := requireEnv("HYPER_API_KEY")
	if err != nil {
		return 1, err
	}
	hyperSession := hyper.NewSession(hyperKey)

	const maxAttempts = 4
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		code, err := runOnce(target, twocaptchaKey, hyperSession)
		if err == nil {
			return code, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "Akamai hard block") {
			return code, err
		}
		if attempt < maxAttempts {
			fmt.Printf("[retry] Akamai 403 — new fingerprint (%d/%d)\n", attempt, maxAttempts)
		}
	}
	return 1, lastErr
}

func runLoginOnly() (int, error) {
	twocaptchaKey, err := requireEnv("TWOCAPTCHA_API_KEY")
	if err != nil {
		return 1, err
	}
	profile := randomFingerprint()
	setFingerprint(profile)
	session, err := makeSession()
	if err != nil {
		return 1, err
	}
	loginURL := fmt.Sprintf("%s%s?gig_ui_locales=en&gig_client_id=%s", gigyaOrigin, loginPagePath, gigyaClientID)
	if err := completeLogin(session, loginURL, "", twocaptchaKey); err != nil {
		return 1, err
	}
	return 0, nil
}
