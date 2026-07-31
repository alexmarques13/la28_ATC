package main

const (
	// UA and client hints are per-session now; see fingerprint.go.

	defaultTarget     = "https://tickets.la28.org/?affiliate=28V&forceLogin=true"
	baseURL           = "https://tickets.la28.org"
	queuePathPrefix   = "/queue"
	maxSensorPosts    = 3
	maxEnqueueRetries = 3
	// Full restarts (fresh client + next approved fingerprint) per account.
	maxRunAttempts = 3

	gigyaAPIKey       = "4_w4CcQ6tKu4jTeDPirnKxnA"
	gigyaDC           = "https://accounts.eu1.gigya.com"
	gigyaOIDCClientID = "xSden-TmSiYYelKvu19SMyTv"
)

var gigyaFIDM = "https://fidm.eu1.gigya.com/oidc/op/v1.0/" + gigyaAPIKey
