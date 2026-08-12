package main

import (
	"fmt"

	"github.com/bogdanfinn/tls-client/profiles"
)

type Fingerprint struct {
	ChromeMajor     int
	UserAgent       string
	SecChUA         string
	SecChUAFullList string
	Platform        string
	PlatformVersion string
	Arch            string
	Bitness         string
	DeviceMemory    string
	TLSProfile      profiles.ClientProfile
	TLSName         string
}

func fingerprintFromProfile(p *FingerprintProfile) Fingerprint {
	if p == nil {
		p = fp()
	}
	tlsProfile := clientProfileFor(p.Impersonate)
	return Fingerprint{
		ChromeMajor:     p.UAMajor,
		UserAgent:       p.UA,
		SecChUA:         p.SecChUA,
		SecChUAFullList: fmt.Sprintf(
			`"Google Chrome";v="%d.0.0.0", "Chromium";v="%d.0.0.0", "Not)A;Brand";v="24.0.0.0"`,
			p.UAMajor, p.UAMajor),
		Platform:        fmt.Sprintf(`"%s"`, p.Platform),
		PlatformVersion: `"19.0.0"`,
		Arch:            `"x86"`,
		Bitness:         `"64"`,
		DeviceMemory:    "8",
		TLSProfile:      tlsProfile,
		TLSName:         p.Impersonate,
	}
}
