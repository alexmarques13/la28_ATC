package main

import (
	"fmt"
	"net"
	"net/http"
	"sync"
)

const cartOpenerAddr = "127.0.0.1:17828"

var (
	cartOpenerOnce sync.Once
	cartOpenerURL  string
	cartOpenerErr  error
)

func ensureCartOpener() (baseURL string, err error) {
	cartOpenerOnce.Do(func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/open-cart", handleOpenCartHTTP)
		ln, e := net.Listen("tcp", cartOpenerAddr)
		if e != nil {
			cartOpenerErr = e
			return
		}
		cartOpenerURL = "http://" + cartOpenerAddr
		go func() {
			_ = http.Serve(ln, mux)
		}()
		fmt.Printf("[discord] cart opener listening on %s/open-cart\n", cartOpenerURL)
	})
	return cartOpenerURL, cartOpenerErr
}

func handleOpenCartHTTP(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	sess := lookupCartSession(sid)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if sess == nil || sess.Client == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Session expired</title>
<p>Cart session expired or the monitor was restarted. Re-run ATC.</p>`))
		return
	}
	fmt.Printf("[discord] Open Cart HTTP hit (session=%s email=%s) — opening Chrome; monitor stays online\n",
		sid, sess.Email)
	go func() {
		if err := sess.Client.OpenCartInBrowser(sess.CartURL); err != nil {
			fmt.Printf("[discord] open cart failed: %v\n", err)
		} else {
			fmt.Println("[discord] Chrome launched — monitor still listening")
		}
	}()
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Opening cart</title>
<p>Opening Chrome with your reserved cart… you can close this tab. The monitor stays running.</p>
<script>setTimeout(function(){ window.close(); }, 800);</script>`))
}

func openCartButtonURL(sessionID string) (string, error) {
	base, err := ensureCartOpener()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/open-cart?sid=%s", base, sessionID), nil
}
