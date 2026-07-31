package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

// liveBrowser holds chromedp cancel funcs so Chrome stays open after
// OpenCartInBrowser returns (Discord button clicks must not shut the bot down).
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

// OpenCartInBrowser launches a visible Chrome window, injects the HTTP client's
// cookies (including HttpOnly 28V.webid), dismisses the legal wall, and opens
// the shopping cart. Returns once the cart page is loaded; Chrome stays open
// until closeLiveBrowsers() or process exit — it does not wait for Ctrl+C.
func (c *Client) OpenCartInBrowser(cartURL string) error {
	if cartURL == "" {
		cartURL = baseURL + "/shoppingCart/?affiliate=28V"
	}
	cookies := c.browserCookieParams()
	if len(cookies) == 0 {
		return fmt.Errorf("no cookies to inject into browser")
	}
	fmt.Printf("[browser] launching Chrome with %d cookies → %s\n", len(cookies), cartURL)
	for _, ck := range cookies {
		if strings.HasPrefix(ck.Name, "28V.") || strings.HasPrefix(ck.Name, "28A.") ||
			strings.HasPrefix(ck.Name, "QueueIT") || ck.Name == "_abck" {
			fmt.Printf("[browser] cookie %s domain=%s path=%s value=%s\n",
				ck.Name, ck.Domain, ck.Path, truncate(ck.Value, 36))
		}
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.WindowSize(1400, 900),
		chromedp.UserAgent(userAgent),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	// Browser lifetime is tied to this context. Chromedp starts Chrome via
	// exec.CommandContext(runCtx) — if Run is given a WithTimeout child and
	// that child is canceled when this function returns, Chrome is killed.
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	retainLiveBrowser(func() {
		browserCancel()
		allocCancel()
	})

	setupDone := make(chan error, 1)
	go func() {
		setupDone <- chromedp.Run(browserCtx,
		network.Enable(),
		// Establish a document on the shop origin, then REPLACE cookies.
		// Do NOT visit the marketing homepage first — it rewrites 28V.webid
		// to NO_SESSION and wipes the ATC session.
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
					if strings.HasPrefix(ck.Name, "28V.") || strings.HasPrefix(ck.Name, "QueueIT") {
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
				case ck.Name == "28V.webid":
					webid = ck.Value
				case ck.Name == "28V.webshop":
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
			fmt.Printf("[browser] verify 28V.webid=%s login=%s queueCookie=%s\n",
				truncate(webid, 40), login, queue)
			if webid == "" || strings.Contains(webid, "NO_SESSION") || login != "1" {
				return fmt.Errorf("browser missing logged-in 28V session (webid=%s login=%s)", truncate(webid, 40), login)
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
		// If consent / soft gate bounced us, force cart once more with cookies intact.
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
			// SPA hydrates cart after cookies; wait for a non-zero cart badge or line item.
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

	fmt.Println("[browser] cart page open — Chrome stays open until you close it or stop the monitor")
	return nil
}

func dismissLegalWall(ctx context.Context) error {
	// Eventim legal / cookie gate — click CONTINUE if present.
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
	seen := map[string]bool{}

	add := func(ck *http.Cookie, pageURL string) {
		if ck == nil || ck.Name == "" || ck.Value == "" {
			return
		}
		id := strings.ToLower(normalizeCookieDomain(ck.Domain, pageURL)) + "|" + ck.Name + "|" + ck.Path + "|" + ck.Value
		if seen[id] {
			return
		}
		seen[id] = true
		raw = append(raw, keyed{ck: ck, url: pageURL})
	}

	if jar, ok := c.http.GetCookieJar().(interface {
		GetAllCookies() map[string][]*http.Cookie
	}); ok {
		for host, cookies := range jar.GetAllCookies() {
			pageURL := cookieURLForHost(host)
			for _, ck := range cookies {
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

	out := make([]*network.CookieParam, 0, len(raw))
	for _, item := range raw {
		ck := item.ck
		path := ck.Path
		if path == "" {
			path = "/"
		}
		domain := normalizeCookieDomain(ck.Domain, item.url)
		// Shop session cookies are issued as Domain=la28.org (parent).
		if isLA28ShopCookie(ck.Name) {
			domain = ".la28.org"
		}
		secure := ck.Secure || strings.HasPrefix(item.url, "https://") || strings.HasSuffix(domain, "la28.org")
		param := &network.CookieParam{
			Name:     ck.Name,
			Value:    ck.Value,
			Path:     path,
			Secure:   secure,
			HTTPOnly: ck.HttpOnly || ck.Name == "28V.webid" || ck.Name == "28A.webid",
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
			// Cross-site shop cookies on .la28.org need None+Secure in Chromium.
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

func isLA28ShopCookie(name string) bool {
	switch {
	case strings.HasPrefix(name, "28V."), strings.HasPrefix(name, "28A."):
		return true
	case strings.HasPrefix(name, "QueueITAccepted"):
		return true
	case name == "_abck", name == "bm_sz", name == "bm_sv", name == "ak_bmsc", name == "bm_so", name == "bm_mi", name == "bm_ss":
		return true
	default:
		return false
	}
}

func normalizeCookieDomain(domain, pageURL string) string {
	domain = strings.TrimSpace(domain)
	if domain != "" {
		if !strings.HasPrefix(domain, ".") && strings.Count(domain, ".") >= 1 {
			// Keep host-only domains as-is; parent domains get a leading dot
			// only when they look like registrable parents (la28.org).
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
