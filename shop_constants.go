package main

import "strings"

const (
	shopAffiliate     = "820"
	queuePathPrefix   = "/queue"
	maxEnqueueRetries = 3
	maxRunAttempts    = 3

	gigyaAPIKey       = "4_w4CcQ6tKu4jTeDPirnKxnA"
	gigyaDC           = "https://accounts.eu1.gigya.com"
	gigyaOIDCClientID = "xSden-TmSiYYelKvu19SMyTv"
)

var gigyaFIDM = "https://fidm.eu1.gigya.com/oidc/op/v1.0/" + gigyaAPIKey

func shopCartURL() string {
	return baseURL + "/shoppingCart/?affiliate=" + shopAffiliate
}

func shopWebidCookie() string  { return shopAffiliate + ".webid" }
func shopWebshopCookie() string { return shopAffiliate + ".webshop" }

var burnedSessionSigs = []string{
	"failed: 403",
	"Access Denied",
	"Reference #",
	"challenge failed",
	"errorCode=-3054",
	"forcibly closed",
	"connection reset",
	"wsasend",
	"wsarecv",
}

func isBurnedSessionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sig := range burnedSessionSigs {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
