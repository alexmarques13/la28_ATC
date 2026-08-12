package main

import (
	"fmt"
	"time"

	hyper "github.com/Hyper-Solutions/hyper-sdk-go/v2"
)

func NewClientFromSession(session *Session, hyperSession *hyper.Session, hyperKey, targetURL, ip string) (*Client, error) {
	if session == nil || session.Client == nil {
		return nil, fmt.Errorf("nil queue session")
	}
	if targetURL == "" {
		targetURL = defaultTarget
	}
	fp := fingerprintFromProfile(fp())
	c := &Client{
		TargetURL:    targetURL,
		PollInterval: 2 * time.Second,
		http:         session.Client,
		jar:          session.jar,
		hyper:        hyperSession,
		hyperKey:     hyperKey,
		ip:           ip,
		fp:           fp,
	}
	if c.ip == "" {
		c.ip = c.getPublicIP()
	}
	fmt.Printf("[shop] continuing on queue session Chrome/%d (%s) ip=%s\n",
		fp.ChromeMajor, fp.TLSName, c.ip)
	return c, nil
}
