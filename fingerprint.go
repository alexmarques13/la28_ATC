package main

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"

	"github.com/bogdanfinn/tls-client/profiles"
)

// Fingerprint is one coherent browser identity: UA, client hints and TLS profile
// that all agree on the same Chrome version.
//
// The set of valid values is dictated by Hyper Solutions, not by us. Their API
// only accepts a Chrome-on-Windows UA (SensorInput, SbsdInput and PixelInput all
// require it), it generates sensor data for the current stable Chrome, and it
// requires every version reference in the request to match, since a mismatch is
// itself a bot signal. So a "new fingerprint" here means a different
// Hyper-approved Chrome build, never an arbitrary UA.
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

// hyperChromeMajors are the Chrome stable majors Hyper currently generates for.
// Refresh when Chrome ships a new stable; the newest majors live at
// https://versionhistory.googleapis.com/v1/chrome/platforms/win/channels/stable/versions/
var hyperChromeMajors = []int{151, 150}

// tlsProfileForChrome picks the TLS fingerprint for a Chrome major. tls-client
// ships nothing newer than Chrome_146, and Hyper documents that a slightly older
// TLS profile is acceptable when no exact match exists.
func tlsProfileForChrome(major int) (profiles.ClientProfile, string) {
	switch {
	case major >= 146:
		return profiles.Chrome_146, "Chrome_146"
	case major >= 144:
		return profiles.Chrome_144, "Chrome_144"
	default:
		return profiles.Chrome_133, "Chrome_133"
	}
}

func newFingerprint(major int) Fingerprint {
	tlsProfile, tlsName := tlsProfileForChrome(major)
	return Fingerprint{
		ChromeMajor: major,
		UserAgent: fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major),
		// Hyper's documented sec-ch-ua template, GREASE brand included verbatim.
		SecChUA: fmt.Sprintf(`"Google Chrome";v="%d", "Chromium";v="%d", "Not)A;Brand";v="24"`,
			major, major),
		SecChUAFullList: fmt.Sprintf(
			`"Google Chrome";v="%d.0.0.0", "Chromium";v="%d.0.0.0", "Not)A;Brand";v="24.0.0.0"`,
			major, major),
		Platform:        `"Windows"`,
		PlatformVersion: `"19.0.0"`,
		Arch:            `"x86"`,
		Bitness:         `"64"`,
		DeviceMemory:    "8",
		TLSProfile:      tlsProfile,
		TLSName:         tlsName,
	}
}

var (
	fingerprintMu  sync.Mutex
	fingerprintIdx = rand.Intn(len(hyperChromeMajors))
	activeFP       = newFingerprint(hyperChromeMajors[fingerprintIdx])
)

// currentFingerprint is the identity every new Client and every raw request
// outside a Client must use, so one run never mixes two Chrome versions.
func currentFingerprint() Fingerprint {
	fingerprintMu.Lock()
	defer fingerprintMu.Unlock()
	return activeFP
}

// rotateFingerprint moves to the next approved identity. Call it when Akamai has
// burned the current one, so the retry does not present the flagged version again.
func rotateFingerprint() Fingerprint {
	fingerprintMu.Lock()
	defer fingerprintMu.Unlock()
	fingerprintIdx = (fingerprintIdx + 1) % len(hyperChromeMajors)
	activeFP = newFingerprint(hyperChromeMajors[fingerprintIdx])
	return activeFP
}

// burnedSessionSigs mark a session Akamai has flagged. These are matched against
// messages this program builds, so "failed: 403" cannot collide with a 403 that
// happens to sit inside a token or event ID in a logged URL.
//
// CapacityControl (-3027) is deliberately absent: that is the shop genuinely
// having no inventory, and a new fingerprint would not change the answer.
var burnedSessionSigs = []string{
	"failed: 403",      // enqueue, Akamai validator or status poll rejected
	"Access Denied",    // Akamai edge block page
	"Reference #",      // Akamai block page reference
	"challenge failed", // Queue-it rejected the challenge token
	"errorCode=-3054",  // shop soft block, immediately followed by socket resets
	"forcibly closed",  // Akamai dropped the TLS connection (Windows wording)
	"connection reset",
	"wsasend",
	"wsarecv",
}

// isBurnedSessionErr reports whether Akamai flagged the session rather than the
// request failing for a transient reason. The cookie jar, TLS handshake and
// sensor context all belong to the flagged identity, so retrying a single step on
// the same client only replays the block — the run has to start over.
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
