package main

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	secChUA         = `"Google Chrome";v="143", "Chromium";v="143", "Not A(Brand";v="24"`
	secChUAPlatform = `"Windows"`

	defaultTarget = "https://tickets.la28.org/?affiliate=28V&forceLogin=true"
	baseURL          = "https://tickets.la28.org"
	queuePathPrefix  = "/queue"
	maxSensorPosts   = 3
	maxEnqueueRetries = 3

	gigyaAPIKey       = "4_w4CcQ6tKu4jTeDPirnKxnA"
	gigyaDC           = "https://accounts.eu1.gigya.com"
	gigyaOIDCClientID = "xSden-TmSiYYelKvu19SMyTv"
)

var gigyaFIDM = "https://fidm.eu1.gigya.com/oidc/op/v1.0/" + gigyaAPIKey
