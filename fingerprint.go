package main

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"

	"github.com/bogdanfinn/tls-client/profiles"
)

// TLS profiles available in bogdanfinn/tls-client on this install.
var tlsProfiles = []string{
	"chrome131",
	"chrome133",
	"chrome144",
	"chrome146",
}

// UA majors Hyper/docs treat as current-ish; TLS may lag behind.
var uaMajors = []int{131, 136, 142, 145, 146, 147, 148, 149}

var acceptLangs = []string{
	"en-US,en;q=0.9",
	"en-US,en;q=0.8",
	"en-US,en;q=0.9,es;q=0.8",
	"en-GB,en;q=0.9,en-US;q=0.8",
	"en-US,en;q=0.9,fr;q=0.5",
}

var screens = []string{
	"1920 x 1080",
	"2560 x 1440",
	"1536 x 864",
	"1440 x 900",
	"1366 x 768",
	"1680 x 1050",
	"2048 x 1152",
}

var brandTemplates = []string{
	`"Google Chrome";v="{v}", "Chromium";v="{v}", "Not)A;Brand";v="24"`,
	`"Chromium";v="{v}", "Google Chrome";v="{v}", "Not)A;Brand";v="24"`,
	`"Google Chrome";v="{v}", "Not)A;Brand";v="8", "Chromium";v="{v}"`,
	`"Not)A;Brand";v="99", "Google Chrome";v="{v}", "Chromium";v="{v}"`,
	`"Google Chrome";v="{v}", "Chromium";v="{v}", "Not-A.Brand";v="24"`,
}

var digitsRe = regexp.MustCompile(`(\d+)`)

// FingerprintProfile is the active TLS + UA combo for a run.
type FingerprintProfile struct {
	Impersonate string
	UAMajor     int
	UA          string
	SecChUA     string
	AcceptLang  string
	Screen      string
	Platform    string
	Key         string
}

var activeFP *FingerprintProfile

func fp() *FingerprintProfile {
	if activeFP == nil {
		p := randomFingerprint()
		activeFP = &p
	}
	return activeFP
}

func setFingerprint(profile FingerprintProfile) {
	activeFP = &profile
}

func randomFingerprint() FingerprintProfile {
	tls := tlsProfiles[rand.Intn(len(tlsProfiles))]
	tlsMaj := 146
	if m := digitsRe.FindStringSubmatch(tls); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &tlsMaj)
	}

	roll := rand.Float64()
	var uaMaj int
	if roll < 0.35 {
		uaMaj = tlsMaj
	} else if roll < 0.75 {
		cands := make([]int, 0, len(uaMajors))
		for _, m := range uaMajors {
			if m >= tlsMaj {
				cands = append(cands, m)
			}
		}
		if len(cands) == 0 {
			cands = uaMajors
		}
		uaMaj = cands[rand.Intn(len(cands))]
	} else {
		uaMaj = uaMajors[rand.Intn(len(uaMajors))]
	}

	brand := strings.ReplaceAll(brandTemplates[rand.Intn(len(brandTemplates))], "{v}", fmt.Sprintf("%d", uaMaj))
	lang := acceptLangs[rand.Intn(len(acceptLangs))]
	screen := screens[rand.Intn(len(screens))]
	ua := fmt.Sprintf(
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
		uaMaj,
	)
	p := FingerprintProfile{
		Impersonate: tls,
		UAMajor:     uaMaj,
		UA:          ua,
		SecChUA:     brand,
		AcceptLang:  lang,
		Screen:      screen,
		Platform:    "Windows",
	}
	p.Key = fmt.Sprintf("%s|%d|%s|%s|%s", p.Impersonate, p.UAMajor, p.SecChUA, p.AcceptLang, p.Screen)
	return p
}

func clientProfileFor(impersonate string) profiles.ClientProfile {
	switch strings.ToLower(impersonate) {
	case "chrome131":
		return profiles.Chrome_131
	case "chrome133", "chrome133a":
		return profiles.Chrome_133
	case "chrome144":
		return profiles.Chrome_144
	case "chrome146":
		return profiles.Chrome_146
	default:
		return profiles.Chrome_146
	}
}
