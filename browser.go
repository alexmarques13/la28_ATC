package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

type liveBrowser struct {
	cancel context.CancelFunc
}

var (
	liveBrowsersMu sync.Mutex
	liveBrowsers   []liveBrowser
)

func retainLiveBrowser(cancel context.CancelFunc) {
	liveBrowsersMu.Lock()
	liveBrowsers = append(liveBrowsers, liveBrowser{cancel: cancel})
	liveBrowsersMu.Unlock()
}

func closeLiveBrowsers() {
	liveBrowsersMu.Lock()
	browsers := liveBrowsers
	liveBrowsers = nil
	liveBrowsersMu.Unlock()
	for _, b := range browsers {
		if b.cancel != nil {
			b.cancel()
		}
	}
}

func (c *Client) OpenCartInBrowser(cartURL string) error {
	if cartURL == "" {
		cartURL = shopCartURL()
	}
	cookies := c.browserCookieParams()
	if len(cookies) == 0 {
		return fmt.Errorf("no cookies to inject into browser")
	}
	fmt.Printf("[browser] launching Chrome with %d cookies → %s\n", len(cookies), cartURL)
	shopPrefix := shopAffiliate + "."
	for _, ck := range cookies {
		if strings.HasPrefix(ck.Name, shopPrefix) || strings.HasPrefix(ck.Name, "28A.") ||
			strings.HasPrefix(ck.Name, "QueueIT") {
			fmt.Printf("[browser] cookie %s domain=%s path=%s value=%s\n",
				ck.Name, ck.Domain, ck.Path, truncate(ck.Value, 36))
		}
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.WindowSize(1400, 900),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	retainLiveBrowser(func() {
		browserCancel()
		allocCancel()
	})

	setupDone := make(chan error, 1)
	go func() {
		setupDone <- chromedp.Run(browserCtx,
		network.Enable(),
		chromedp.Navigate(baseURL+"/robots.txt"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := storage.ClearCookies().Do(ctx); err != nil {
				fmt.Printf("[browser] clear cookies warning: %v\n", err)
			}
			ok, fail := 0, 0
			for _, ck := range cookies {
				p := network.SetCookie(ck.Name, ck.Value).
					WithPath(ck.Path).
					WithSecure(ck.Secure).
					WithHTTPOnly(ck.HTTPOnly)
				if ck.Domain != "" {
					p = p.WithDomain(ck.Domain)
				} else if ck.URL != "" {
					p = p.WithURL(ck.URL)
				} else {
					p = p.WithURL(baseURL + "/")
				}
				if ck.SameSite != "" {
					p = p.WithSameSite(ck.SameSite)
				}
				if ck.Expires != nil {
					p = p.WithExpires(ck.Expires)
				}
				if err := p.Do(ctx); err != nil {
					fail++
					if strings.HasPrefix(ck.Name, shopPrefix) || strings.HasPrefix(ck.Name, "QueueIT") {
						fmt.Printf("[browser] set-cookie FAIL %s: %v\n", ck.Name, err)
					}
					continue
				}
				ok++
			}
			fmt.Printf("[browser] injected %d cookies (%d failed)\n", ok, fail)
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			got, err := network.GetCookies().WithURLs([]string{baseURL + "/", cartURL}).Do(ctx)
			if err != nil {
				return err
			}
			var webid, login, queue string
			for _, ck := range got {
				switch {
				case ck.Name == shopWebidCookie():
					webid = ck.Value
				case ck.Name == shopWebshopCookie():
					if d, err := decodeWebshop(ck.Value); err == nil {
						vals, _ := url.ParseQuery(d)
						login = vals.Get("login")
					}
				case strings.HasPrefix(ck.Name, "QueueITAccepted"):
					queue = ck.Name
				}
			}
			if webid != "" {
				if raw, err := decodeMaybeB64(webid); err == nil {
					webid = raw
				}
			}
			fmt.Printf("[browser] verify %s=%s login=%s queueCookie=%s\n",
				shopWebidCookie(), truncate(webid, 40), login, queue)
			if webid == "" || strings.Contains(webid, "NO_SESSION") || login != "1" {
				return fmt.Errorf("browser missing logged-in %s session (webid=%s login=%s)", shopAffiliate, truncate(webid, 40), login)
			}
			return nil
		}),
		chromedp.Navigate(cartURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(1500*time.Millisecond),
		chromedp.ActionFunc(dismissLegalWall),
		chromedp.Sleep(800*time.Millisecond),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var href, title, bodyText string
			if err := chromedp.Evaluate(`location.href`, &href).Do(ctx); err != nil {
				return err
			}
			_ = chromedp.Evaluate(`document.title`, &title).Do(ctx)
			_ = chromedp.Evaluate(`(document.body && document.body.innerText || '').slice(0, 800)`, &bodyText).Do(ctx)
			fmt.Printf("[browser] landed title=%q url=%s\n", title, truncate(href, 120))
			fmt.Printf("[browser] page text: %s\n", truncate(strings.ReplaceAll(bodyText, "\n", " | "), 280))
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var href string
			_ = chromedp.Evaluate(`location.href`, &href).Do(ctx)
			if !strings.Contains(href, "shoppingCart") {
				if err := chromedp.Navigate(cartURL).Do(ctx); err != nil {
					return err
				}
				_ = chromedp.WaitReady("body", chromedp.ByQuery).Do(ctx)
				_ = chromedp.Sleep(1200 * time.Millisecond).Do(ctx)
			}
			_ = dismissLegalWall(ctx)
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				var ok bool
				_ = chromedp.Evaluate(`(() => {
					const t = (document.body && document.body.innerText) || '';
					if (/archery|carson|standard|ticket|€|\$\d/i.test(t) && !/log in below/i.test(t)) {
						const m = t.match(/to shopping cart\s*(\d+)/i) || t.match(/\b(\d+)\s*tickets?/i);
						if (m && m[1] && m[1] !== '0') return true;
						if (/order overview|shopping cart|checkout|remove/i.test(t) && !/0 tickets/i.test(t)) return true;
					}
					const badge = [...document.querySelectorAll('a,button,span,div')].find(el => /shopping cart/i.test(el.innerText||''));
					if (badge && /\b[1-9]\d*\b/.test(badge.innerText||'')) return true;
					return false;
				})()`, &ok).Do(ctx)
				if ok {
					fmt.Println("[browser] cart contents detected in page")
					return nil
				}
				_ = chromedp.Sleep(500 * time.Millisecond).Do(ctx)
			}
			fmt.Println("[browser] warning: could not confirm cart line-items yet — leaving window open for manual review")
			return nil
		}),
		)
	}()

	var err error
	select {
	case err = <-setupDone:
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("open cart browser: setup timed out (Chrome left running)")
	}
	if err != nil {
		return fmt.Errorf("open cart browser: %w", err)
	}

	verifyShopNavigation(browserCtx)

	fmt.Println("[browser] cart page open — Chrome stays open until you close it or stop the monitor")
	return nil
}

func verifyShopNavigation(browserCtx context.Context) {
	tabCtx, cancel := chromedp.NewContext(browserCtx)
	defer cancel()
	ctx, tcancel := context.WithTimeout(tabCtx, 45*time.Second)
	defer tcancel()

	var title, body string
	err := chromedp.Run(ctx,
		chromedp.Navigate(baseURL+"/?affiliate="+shopAffiliate),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(1200*time.Millisecond),
		chromedp.Evaluate(`document.title`, &title),
		chromedp.Evaluate(`(document.body && document.body.innerText || '').slice(0, 200)`, &body),
	)
	if err != nil {
		fmt.Printf("[browser] shop entry check warning: %v\n", err)
		return
	}
	if strings.Contains(title, "Access Denied") || strings.Contains(body, "Access Denied") {
		fmt.Printf("[browser] WARNING: Akamai denied the shop entry navigation (title=%q) — clicking through the modals will fail\n", title)
		return
	}
	fmt.Printf("[browser] shop entry navigation ok (title=%q)\n", truncate(title, 60))
}

func dismissLegalWall(ctx context.Context) error {
	var clicked bool
	err := chromedp.Evaluate(`(() => {
		const nodes = [...document.querySelectorAll('button, a, [role="button"], input[type="button"], input[type="submit"]')];
		const btn = nodes.find(el => /continue|accept|agree|i understand/i.test((el.innerText || el.value || '').trim()));
		if (!btn) return false;
		btn.click();
		return true;
	})()`, &clicked).Do(ctx)
	if err != nil {
		return err
	}
	if clicked {
		fmt.Println("[browser] dismissed legal/consent wall")
		_ = chromedp.Sleep(1000 * time.Millisecond).Do(ctx)
	}
	return nil
}

func decodeMaybeB64(v string) (string, error) {
	raw, err := decodeWebshop(v)
	if err != nil {
		return v, err
	}
	if strings.HasPrefix(raw, "28") || strings.Contains(raw, "=") {
		return raw, nil
	}
	return v, nil
}

func (c *Client) browserCookieParams() []*network.CookieParam {
	type keyed struct {
		ck  *http.Cookie
		url string
	}
	var raw []keyed
	index := map[string]int{}
	skippedBot := 0

	add := func(ck *http.Cookie, pageURL string) {
		if ck == nil || ck.Name == "" || ck.Value == "" {
			return
		}
		if isAkamaiBotCookie(ck.Name) {
			skippedBot++
			return
		}
		id := strings.ToLower(effectiveCookieDomain(ck.Name, ck.Domain, pageURL)) + "|" + ck.Name + "|" + ck.Path
		if i, ok := index[id]; ok {
			raw[i] = keyed{ck: ck, url: pageURL}
			return
		}
		index[id] = len(raw)
		raw = append(raw, keyed{ck: ck, url: pageURL})
	}

	if jar, ok := c.http.GetCookieJar().(interface {
		GetAllCookies() map[string][]*http.Cookie
	}); ok {
		all := jar.GetAllCookies()
		hosts := make([]string, 0, len(all))
		for host := range all {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts {
			pageURL := cookieURLForHost(host)
			for _, ck := range all[host] {
				add(ck, pageURL)
			}
		}
	}

	for _, pageURL := range []string{
		baseURL + "/",
		"https://tickets.la28.org/",
		"https://la28.org/",
		"https://la28id.la28.org/",
		"https://accounts.eu1.gigya.com/",
		"https://fidm.eu1.gigya.com/",
	} {
		u := mustParseURL(pageURL)
		for _, ck := range c.http.GetCookies(u) {
			add(ck, pageURL)
		}
	}

	if skippedBot > 0 {
		fmt.Printf("[browser] skipped %d Akamai bot cookie(s) — Chrome will mint its own\n", skippedBot)
	}

	out := make([]*network.CookieParam, 0, len(raw))
	for _, item := range raw {
		ck := item.ck
		path := ck.Path
		if path == "" {
			path = "/"
		}
		domain := effectiveCookieDomain(ck.Name, ck.Domain, item.url)
		secure := ck.Secure || strings.HasPrefix(item.url, "https://") || strings.HasSuffix(domain, "la28.org")
		param := &network.CookieParam{
			Name:     ck.Name,
			Value:    ck.Value,
			Path:     path,
			Secure:   secure,
			HTTPOnly: ck.HttpOnly || ck.Name == shopWebidCookie() || ck.Name == "28A.webid",
		}
		if domain != "" {
			param.Domain = domain
		} else {
			param.URL = item.url
			if param.URL == "" {
				param.URL = baseURL + "/"
			}
		}
		switch ck.SameSite {
		case http.SameSiteLaxMode:
			param.SameSite = network.CookieSameSiteLax
		case http.SameSiteStrictMode:
			param.SameSite = network.CookieSameSiteStrict
		case http.SameSiteNoneMode:
			param.SameSite = network.CookieSameSiteNone
		default:
			if strings.HasSuffix(domain, "la28.org") {
				param.SameSite = network.CookieSameSiteNone
			}
		}
		if !ck.Expires.IsZero() && ck.Expires.After(time.Now()) {
			exp := cdp.TimeSinceEpoch(ck.Expires)
			param.Expires = &exp
		}
		out = append(out, param)
	}
	return out
}

func effectiveCookieDomain(name, domain, pageURL string) string {
	if isLA28ShopCookie(name) {
		return ".la28.org"
	}
	return normalizeCookieDomain(domain, pageURL)
}

func isLA28ShopCookie(name string) bool {
	switch {
	case strings.HasPrefix(name, shopAffiliate+"."), strings.HasPrefix(name, "28A."):
		return true
	case strings.HasPrefix(name, "QueueITAccepted"):
		return true
	default:
		return false
	}
}

func isAkamaiBotCookie(name string) bool {
	switch strings.ToLower(name) {
	case "_abck", "ak_bmsc", "bm_sz", "bm_sv", "bm_so", "bm_mi", "bm_ss", "bm_lso",
		"sbsd", "sbsd_o", "sec_cpt":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "sec_cpt")
}

func normalizeCookieDomain(domain, pageURL string) string {
	domain = strings.TrimSpace(domain)
	if domain != "" {
		if !strings.HasPrefix(domain, ".") && strings.Count(domain, ".") >= 1 {
			if domain == "la28.org" || domain == "gigya.com" {
				return "." + domain
			}
		}
		if domain == ".la28.org" || domain == ".gigya.com" {
			return domain
		}
		return domain
	}
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	switch {
	case host == "tickets.la28.org" || host == "la28id.la28.org" || host == "la28.org":
		return ".la28.org"
	case strings.HasSuffix(host, ".gigya.com"):
		return ".gigya.com"
	default:
		return host
	}
}

func cookieURLForHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.Trim(host, ".")
	if host == "" {
		return baseURL + "/"
	}
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	switch {
	case strings.Contains(host, "gigya.com"):
		return "https://" + strings.TrimPrefix(host, "www.") + "/"
	case strings.Contains(host, "la28.org"):
		if strings.Contains(host, "la28id") {
			return "https://la28id.la28.org/"
		}
		return "https://tickets.la28.org/"
	default:
		return "https://" + host + "/"
	}
}
