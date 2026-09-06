package enrich

import (
	"regexp"
	"sort"
	"strings"
)

// Devices.
const (
	DeviceDesktop = "Desktop"
	DeviceMobile  = "Mobile"
	DeviceTablet  = "Tablet"
)

// UA is what we keep from a user agent string.
type UA struct {
	Browser string
	OS      string
	Device  string
	Bot     bool
	// BotName is the crawler's display name when Bot is set, e.g. "ChatGPT"
	// or "Googlebot". "Other bot" when it matched the generic pattern.
	BotName string
	// AIBot marks crawlers that feed a large language model, either training
	// on the page or fetching it to answer a prompt.
	AIBot bool
}

var botRe = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|headless|lighthouse|pingdom|uptime|monitor|curl/|wget/|python-requests|go-http-client|facebookexternalhit|preview|scrapy|http-client|okhttp|java/|libwww|axios/|node-fetch|got \(|fetch/`)

// Crawlers worth naming. Ordered, first match wins, so specific tokens come
// before the families they belong to. AI marks a crawler that feeds a model.
var crawlers = []struct {
	token string
	name  string
	ai    bool
}{
	// Crawlers that fetch a page to answer a prompt, or to train on it.
	{"GPTBot", "GPTBot", true},
	{"OAI-SearchBot", "OpenAI Search", true},
	{"ChatGPT-User", "ChatGPT", true},
	{"ClaudeBot", "ClaudeBot", true},
	{"Claude-User", "Claude", true},
	{"Claude-SearchBot", "Claude Search", true},
	{"anthropic-ai", "Anthropic", true},
	{"PerplexityBot", "Perplexity", true},
	{"Perplexity-User", "Perplexity", true},
	{"Google-Extended", "Google Extended", true},
	{"GoogleOther", "GoogleOther", true},
	{"Gemini", "Gemini", true},
	{"Applebot-Extended", "Applebot Extended", true},
	{"Bytespider", "Bytespider", true},
	{"CCBot", "Common Crawl", true},
	{"Amazonbot", "Amazonbot", true},
	{"meta-externalagent", "Meta AI", true},
	{"FacebookBot", "Meta AI", true},
	{"Diffbot", "Diffbot", true},
	{"cohere-ai", "Cohere", true},
	{"YouBot", "You.com", true},
	{"Timpibot", "Timpi", true},
	{"omgili", "Webz.io", true},
	{"ImagesiftBot", "ImageSift", true},
	{"MistralAI", "Mistral", true},
	{"DeepSeek", "DeepSeek", true},
	{"xAI", "xAI", true},
	{"Grok", "Grok", true},

	// Ordinary search and social crawlers.
	{"Googlebot", "Googlebot", false},
	{"AdsBot-Google", "Googlebot Ads", false},
	{"bingbot", "Bingbot", false},
	{"BingPreview", "Bingbot", false},
	{"Slurp", "Yahoo Slurp", false},
	{"DuckDuckBot", "DuckDuckBot", false},
	{"DuckAssistBot", "DuckDuckGo Assist", true},
	{"Baiduspider", "Baiduspider", false},
	{"YandexBot", "YandexBot", false},
	{"Applebot", "Applebot", false},
	{"AhrefsBot", "AhrefsBot", false},
	{"SemrushBot", "SemrushBot", false},
	{"MJ12bot", "Majestic", false},
	{"DotBot", "DotBot", false},
	{"PetalBot", "PetalBot", false},
	{"Screaming Frog", "Screaming Frog", false},
	{"SeekportBot", "Seekport", false},
	{"facebookexternalhit", "Facebook", false},
	{"Twitterbot", "Twitterbot", false},
	{"LinkedInBot", "LinkedInBot", false},
	{"Slackbot", "Slackbot", false},
	{"Discordbot", "Discordbot", false},
	{"TelegramBot", "TelegramBot", false},
	{"WhatsApp", "WhatsApp", false},
	{"Pinterest", "Pinterest", false},
	{"redditbot", "Redditbot", false},
	{"Mastodon", "Mastodon", false},

	// Monitoring and tooling.
	{"Lighthouse", "Lighthouse", false},
	{"Chrome-Lighthouse", "Lighthouse", false},
	{"UptimeRobot", "UptimeRobot", false},
	{"Pingdom", "Pingdom", false},
	{"Better Uptime", "Better Stack", false},
	{"HeadlessChrome", "Headless Chrome", false},
	{"curl/", "curl", false},
	{"Wget/", "wget", false},
	{"python-requests", "python-requests", false},
	{"Go-http-client", "Go http client", false},
	{"axios/", "axios", false},
	{"node-fetch", "node-fetch", false},
	{"okhttp", "okhttp", false},
	{"Scrapy", "Scrapy", false},
}

// AICrawlerNames is every display name classifyBot may return for a crawler
// that feeds a language model, sorted and deduplicated. Rollups use it to
// build the "AI crawlers" dimension: the ai flag is a property of the name,
// not of the stored row, so it is resolved from this list rather than from a
// column nobody would be able to backfill.
var AICrawlerNames = func() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range crawlers {
		if c.ai && !seen[c.name] {
			seen[c.name] = true
			out = append(out, c.name)
		}
	}
	sort.Strings(out)
	return out
}()

// IsAICrawler reports whether name is one of the AI crawlers.
func IsAICrawler(name string) bool {
	i := sort.SearchStrings(AICrawlerNames, name)
	return i < len(AICrawlerNames) && AICrawlerNames[i] == name
}

// classifyBot names a crawler from its user agent.
func classifyBot(ua string) (name string, ai bool) {
	for _, c := range crawlers {
		if containsFold(ua, c.token) {
			return c.name, c.ai
		}
	}
	return "Other bot", false
}

// containsFold is a case-insensitive strings.Contains. User agents are not
// consistent about capitalisation ("bingbot" vs "BingBot").
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// Ordered: the first match wins, so more specific tokens come first.
var browsers = []struct{ token, name string }{
	{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"EdgiOS/", "Edge"},
	{"OPR/", "Opera"}, {"Opera", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"Vivaldi/", "Vivaldi"}, {"Brave", "Brave"}, {"Arc/", "Arc"},
	{"DuckDuckGo/", "DuckDuckGo"},
	{"FxiOS/", "Firefox"}, {"Firefox/", "Firefox"},
	{"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Chromium/", "Chromium"},
	{"Safari/", "Safari"},
}

// ParseUA extracts browser, OS and device from a user agent. Unknown values
// are "Other".
func ParseUA(ua string, screenWidth int) UA {
	out := UA{Browser: "Other", OS: "Other", Device: DeviceDesktop}
	if ua == "" {
		return out
	}
	if botRe.MatchString(ua) {
		out.Bot = true
		out.BotName, out.AIBot = classifyBot(ua)
		return out
	}
	for _, b := range browsers {
		if strings.Contains(ua, b.token) {
			out.Browser = b.name
			break
		}
	}
	// Safari's token appears in every WebKit UA; only count it when nothing
	// more specific matched and it really is Safari.
	if out.Browser == "Safari" && !strings.Contains(ua, "Version/") {
		out.Browser = "Other"
	}
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPod"):
		out.OS, out.Device = "iOS", DeviceMobile
	case strings.Contains(ua, "iPad"):
		out.OS, out.Device = "iPadOS", DeviceTablet
	case strings.Contains(ua, "Android"):
		out.OS = "Android"
		if strings.Contains(ua, "Mobile") {
			out.Device = DeviceMobile
		} else {
			out.Device = DeviceTablet
		}
	case strings.Contains(ua, "Windows"):
		out.OS = "Windows"
	case strings.Contains(ua, "CrOS"):
		out.OS = "ChromeOS"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		out.OS = "macOS"
		// iPadOS Safari reports as a Mac; a touch-sized screen gives it away.
		if screenWidth > 0 && screenWidth <= 1024 && strings.Contains(ua, "Safari/") && !strings.Contains(ua, "Chrome/") && screenWidth < 1100 {
			out.OS, out.Device = "iPadOS", DeviceTablet
		}
	case strings.Contains(ua, "Linux"):
		out.OS = "Linux"
	}
	// Screen width refines the device class for anything not already mobile.
	if out.Device == DeviceDesktop && screenWidth > 0 {
		switch {
		case screenWidth < 600:
			out.Device = DeviceMobile
		case screenWidth < 1024 && out.OS != "Windows" && out.OS != "macOS" && out.OS != "Linux" && out.OS != "ChromeOS":
			out.Device = DeviceTablet
		}
	}
	return out
}
