// Package web implements web tools: fetching a page as text, and searching
// via DuckDuckGo's HTML results page (no paid search API, no HTML-parser
// dependency — see htmltext.go for the tag-stripping used by both).
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/tools"
)

const (
	defaultFetchMaxChars = 6_000
	minFetchMaxChars     = 500
	maxFetchMaxChars     = 20_000
	maxResponseBytes     = 20 << 20 // 20MB safety cap while reading a response body
	requestTimeout       = 15 * time.Second
	userAgent            = "Mozilla/5.0 (compatible; souz-agent/1.0; +https://github.com/)"

	defaultSearchBaseURL = "https://html.duckduckgo.com/html/"
	defaultSearchResults = 5
	maxSearchResultsCap  = 10
)

// Config configures the web tool set. HTTPClient and SearchBaseURL are
// overridable for tests; zero values use sane production defaults.
type Config struct {
	HTTPClient    *http.Client
	SearchBaseURL string
}

// New builds the web tool set: Fetch and Search.
func New(cfg Config) []tools.Tool {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	searchBaseURL := cfg.SearchBaseURL
	if searchBaseURL == "" {
		searchBaseURL = defaultSearchBaseURL
	}
	return []tools.Tool{
		&Fetch{client: client},
		&Search{client: client, baseURL: searchBaseURL},
	}
}

// --- Fetch ---

type Fetch struct{ client *http.Client }

var _ tools.Tool = (*Fetch)(nil)

func (t *Fetch) Name() string { return "web_fetch" }
func (t *Fetch) Description() string {
	return "Fetches a web page over HTTP(S) and returns its visible text content, HTML tags stripped."
}
func (t *Fetch) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "Page URL, must start with http:// or https://"},
			"max_chars": {"type": "integer", "description": "Cap on returned characters; defaults to 6000, clamped to [500, 20000]"}
		},
		"required": ["url"]
	}`)
}

func (t *Fetch) Execute(ctx context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	target, err := tools.ArgString(args, "url", "")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return "", fmt.Errorf("url must start with http:// or https://, got %q", target)
	}
	maxChars, err := tools.ArgInt(args, "max_chars", defaultFetchMaxChars)
	if err != nil {
		return "", err
	}
	maxChars = clamp(maxChars, minFetchMaxChars, maxFetchMaxChars)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %w", target, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("fetch %q: read body: %w", target, err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch %q: status %d", target, resp.StatusCode)
	}

	text := body2text(body, resp.Header.Get("Content-Type"))
	if runes := []rune(text); len(runes) > maxChars {
		text = string(runes[:maxChars]) + fmt.Sprintf("\n\n[truncated: page exceeds %d characters]", maxChars)
	}
	return text, nil
}

func body2text(body []byte, contentType string) string {
	if strings.Contains(contentType, "html") {
		return htmlToText(string(body))
	}
	return strings.TrimSpace(string(body))
}

func clamp(v, min, max int) int {
	if v <= 0 {
		return min
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// --- Search ---

type Search struct {
	client  *http.Client
	baseURL string
}

var _ tools.Tool = (*Search)(nil)

func (t *Search) Name() string { return "web_search" }
func (t *Search) Description() string {
	return "Searches the web (via DuckDuckGo) and returns matching titles, URLs, and snippets."
}
func (t *Search) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"max_results": {"type": "integer", "description": "Cap on results; defaults to 5, max 10"}
		},
		"required": ["query"]
	}`)
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

var (
	resultLinkRe    = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*\bresult__a\b[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	resultSnippetRe = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</a>`)
)

func (t *Search) Execute(ctx context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	query, err := tools.ArgString(args, "query", "")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	maxResults, err := tools.ArgInt(args, "max_results", defaultSearchResults)
	if err != nil {
		return "", err
	}
	maxResults = clamp(maxResults, 1, maxSearchResultsCap)

	reqURL := t.baseURL + "?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("search %q: %w", query, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search %q: %w", query, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("search %q: read body: %w", query, err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("search %q: status %d", query, resp.StatusCode)
	}

	results := parseSearchResults(string(body), maxResults)
	out, err := json.Marshal(results)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseSearchResults(page string, maxResults int) []searchResult {
	links := resultLinkRe.FindAllStringSubmatch(page, -1)
	snippets := resultSnippetRe.FindAllStringSubmatch(page, -1)

	results := make([]searchResult, 0, maxResults)
	for i, link := range links {
		if len(results) >= maxResults {
			break
		}
		target := decodeDuckDuckGoRedirect(link[1])
		if target == "" {
			continue
		}
		snippet := ""
		if i < len(snippets) {
			snippet = htmlToText(snippets[i][1])
		}
		results = append(results, searchResult{
			Title:   htmlToText(link[2]),
			URL:     target,
			Snippet: snippet,
		})
	}
	return results
}

// decodeDuckDuckGoRedirect unwraps DuckDuckGo's "//duckduckgo.com/l/?uddg=<encoded>"
// result-link redirect down to the real target URL. Returns "" if href isn't
// a usable link.
func decodeDuckDuckGoRedirect(href string) string {
	href = html2attr(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err == nil {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			return uddg
		}
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}

// html2attr decodes HTML entities that appear inside an href attribute
// (DuckDuckGo escapes "&" as "&amp;" between query parameters).
func html2attr(s string) string {
	return strings.ReplaceAll(s, "&amp;", "&")
}
