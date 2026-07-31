package main

const (
	// Hyper sensor generation expects this GREASE brand form + a supported Chrome major.
	// Real browser ATC used Chrome/150 Not;A=Brand — keep payload from that capture,
	// but UA/sec-ch-ua must stay Hyper-compatible or enqueue/sensors 403.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	secChUA = `"Google Chrome";v="149", "Chromium";v="149", "Not)A;Brand";v="24"`
	secChUAFullVersionList = `"Google Chrome";v="149.0.0.0", "Chromium";v="149.0.0.0", "Not)A;Brand";v="10.0.0.0"`
	secChUAPlatform        = `"Windows"`
	secChUAPlatformVersion = `"15.0.0"`
	secChUAArch            = `"x86"`
	secChUABitness         = `"64"`
	secChDeviceMemory      = `8`

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
