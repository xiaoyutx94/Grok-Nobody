package grokregister

import (
	"fmt"
	"math/rand"
	"strings"
)

// BrowserProfile holds a complete TLS fingerprint identity for a single request session.
type BrowserProfile struct {
	Browser  string // azuretls browser key: "chrome", "firefox", "safari", "edge"
	OS       string // "windows", "macos", "linux"
	Version  string // browser version string
	UA       string // full User-Agent
	SecCHUA  string // Sec-CH-UA header (Chromium-based only)
	Platform string // Sec-CH-UA-Platform value
}

// Browser version pools — recent stable versions (as of 2026-07)
// Chrome ~153 stable (Jul 2026); older kept for natural diversity.
// Firefox ~155 stable; Safari uses macOS Tahoe year-based versioning (26.x).
// Edge follows Chrome versioning. Newer versions weighted by being later in the
// slice matters not (uniform pick) — we keep a recent-heavy list.
var browserVersions = map[string][]string{
	"chrome":  {"148.0.0.0", "149.0.0.0", "150.0.0.0", "151.0.0.0", "152.0.0.0", "153.0.0.0"},
	"firefox": {"150.0", "151.0", "152.0", "153.0", "154.0", "155.0"},
	"safari":  {"18.2", "18.4", "18.6", "26.0", "26.1", "26.2", "26.3", "26.4"},
	"edge":    {"148.0.0.0", "149.0.0.0", "150.0.0.0", "151.0.0.0", "152.0.0.0", "153.0.0.0"},
}

// Linux distribution names for Firefox UA diversity (empty = generic)
var linuxDistros = []string{"", "", "", "Ubuntu; ", "Fedora; "}

var allBrowsers = []string{"chrome", "firefox", "safari", "edge"}
var allOSes = []string{"windows", "macos", "linux"}

type osWeight struct {
	os     string
	weight int // cumulative weight out of 100
}

var osWeights = []osWeight{
	{"windows", 75},
	{"macos", 95},
	{"linux", 100},
}

// browser → valid OSes
var browserToOS = map[string][]string{
	"chrome":  {"windows", "macos", "linux"},
	"firefox": {"windows", "macos", "linux"},
	"safari":  {"macos"},
	"edge":    {"windows", "macos", "linux"},
}

// OS → valid browsers (reverse mapping)
var osToBrowser = map[string][]string{
	"windows": {"chrome", "firefox", "edge"},
	"macos":   {"chrome", "firefox", "safari", "edge"},
	"linux":   {"chrome", "firefox", "edge"},
}

func pickWeightedOS() string {
	r := rand.Intn(100)
	for _, ow := range osWeights {
		if r < ow.weight {
			return ow.os
		}
	}
	return "windows"
}

func pickBrowserForOS(osType string) string {
	browsers := osToBrowser[osType]
	if len(browsers) == 0 {
		return "chrome"
	}
	return browsers[rand.Intn(len(browsers))]
}

func pickOSForBrowser(browser string) string {
	oses := browserToOS[browser]
	if len(oses) == 0 {
		return "windows"
	}
	return oses[rand.Intn(len(oses))]
}

func isValidCombo(browser, osType string) bool {
	for _, v := range browserToOS[browser] {
		if v == osType {
			return true
		}
	}
	return false
}

// PickBrowserProfile selects a random version and generates a complete profile.
// browser: "chrome","firefox","safari","edge","random" (empty = "random")
// osType:  "windows","macos","linux","random" (empty = "random")
//
// Matching priority: OS is more fundamental than browser.
//   - Both random: pick OS by weight, then pick browser valid for that OS
//   - OS fixed + browser random: pick browser valid for that OS
//   - Browser fixed + OS random: pick OS valid for that browser
//   - Both fixed but incompatible: keep OS, adjust browser
func PickBrowserProfile(browser, osType string) BrowserProfile {
	browserRandom := browser == "" || browser == "random"
	osRandom := osType == "" || osType == "random"

	if !browserRandom {
		browser = strings.ToLower(browser)
	}
	if !osRandom {
		osType = strings.ToLower(osType)
	}

	switch {
	case browserRandom && osRandom:
		osType = pickWeightedOS()
		browser = pickBrowserForOS(osType)
	case browserRandom && !osRandom:
		browser = pickBrowserForOS(osType)
	case !browserRandom && osRandom:
		osType = pickOSForBrowser(browser)
	default:
		if !isValidCombo(browser, osType) {
			browser = pickBrowserForOS(osType)
		}
	}

	versions := browserVersions[browser]
	if len(versions) == 0 {
		browser = "chrome"
		versions = browserVersions["chrome"]
	}
	ver := versions[rand.Intn(len(versions))]

	p := BrowserProfile{
		Browser: browser,
		OS:      osType,
		Version: ver,
	}

	switch browser {
	case "chrome":
		p.UA = chromeUA(ver, osType)
		p.SecCHUA = chromeSecCHUA(ver)
		p.Platform = osPlatformName(osType)
	case "edge":
		p.UA = edgeUA(ver, osType)
		p.SecCHUA = edgeSecCHUA(ver)
		p.Platform = osPlatformName(osType)
	case "firefox":
		p.UA = firefoxUA(ver, osType)
	case "safari":
		p.UA = safariUA(ver)
		p.Platform = "macOS"
	}

	return p
}

func osPlatformName(os string) string {
	switch os {
	case "macos":
		return "macOS"
	case "linux":
		return "Linux"
	default:
		return "Windows"
	}
}

func osUAFragment(osType string) string {
	switch osType {
	case "macos":
		return "Macintosh; Intel Mac OS X 10_15_7"
	case "linux":
		return "X11; Linux x86_64"
	default:
		return "Windows NT 10.0; Win64; x64"
	}
}

func chromeUA(ver, osType string) string {
	return fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", osUAFragment(osType), ver)
}

func chromeSecCHUA(ver string) string {
	major := strings.Split(ver, ".")[0]
	return fmt.Sprintf(`"Chromium";v="%s", "Not/A)Brand";v="24", "Google Chrome";v="%s"`, major, major)
}

func edgeUA(ver, osType string) string {
	return fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36 Edg/%s", osUAFragment(osType), ver, ver)
}

func edgeSecCHUA(ver string) string {
	major := strings.Split(ver, ".")[0]
	return fmt.Sprintf(`"Chromium";v="%s", "Not/A)Brand";v="24", "Microsoft Edge";v="%s"`, major, major)
}

func firefoxUA(ver, osType string) string {
	major := strings.Split(ver, ".")[0]
	if osType == "linux" {
		distro := linuxDistros[rand.Intn(len(linuxDistros))]
		return fmt.Sprintf("Mozilla/5.0 (X11; %sLinux x86_64; rv:%s) Gecko/20100101 Firefox/%s", distro, major+".0", ver)
	}
	return fmt.Sprintf("Mozilla/5.0 (%s; rv:%s) Gecko/20100101 Firefox/%s", osUAFragment(osType), major+".0", ver)
}

func safariUA(ver string) string {
	return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/%s Safari/605.1.15", ver)
}

// IsChromiumBased returns true for Chrome-based browsers that support Sec-CH-UA headers.
func (p *BrowserProfile) IsChromiumBased() bool {
	return p.Browser == "chrome" || p.Browser == "edge"
}
