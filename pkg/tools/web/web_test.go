package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"souz.ru/souz-go/pkg/agent"
)

func rawArgs(t *testing.T, kv map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]json.RawMessage, len(kv))
	for k, v := range kv {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %q: %v", k, err)
		}
		out[k] = b
	}
	return out
}

func TestFetch_StripsHTMLAndTruncates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><style>body{color:red}</style><script>evil()</script></head>` +
			`<body><h1>Title</h1><p>Hello &amp; welcome.</p></body></html>`))
	}))
	defer server.Close()

	fetch := New(Config{})[0]
	got, err := fetch.Execute(context.Background(), rawArgs(t, map[string]any{"url": server.URL}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(got, "evil()") || strings.Contains(got, "color:red") {
		t.Errorf("expected script/style content stripped, got %q", got)
	}
	if !strings.Contains(got, "Title") || !strings.Contains(got, "Hello & welcome.") {
		t.Errorf("expected visible text with decoded entities, got %q", got)
	}
}

func TestFetch_TruncatesToMaxChars(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("a", 2000)))
	}))
	defer server.Close()

	fetch := New(Config{})[0]
	got, err := fetch.Execute(context.Background(), rawArgs(t, map[string]any{"url": server.URL, "max_chars": 500}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "[truncated") {
		t.Errorf("expected truncation marker, got suffix %q", got[len(got)-50:])
	}
}

func TestFetch_RejectsNonHTTPURL(t *testing.T) {
	fetch := New(Config{})[0]
	_, err := fetch.Execute(context.Background(), rawArgs(t, map[string]any{"url": "ftp://example.com"}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for non-http(s) URL")
	}
}

func TestFetch_PropagatesHTTPErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	fetch := New(Config{})[0]
	_, err := fetch.Execute(context.Background(), rawArgs(t, map[string]any{"url": server.URL}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestSearch_ParsesDuckDuckGoResultPage(t *testing.T) {
	page := `<html><body>
		<div class="result results_links results_links_deep web-result">
			<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&amp;rut=abc">Example Domain One</a>
			<a class="result__snippet">First snippet &amp; more.</a>
		</div>
		<div class="result results_links results_links_deep web-result">
			<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fpage2&amp;rut=def">Example Domain Two</a>
			<a class="result__snippet">Second snippet.</a>
		</div>
	</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "example query" {
			t.Errorf("unexpected query: %s", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(page))
	}))
	defer server.Close()

	search := New(Config{SearchBaseURL: server.URL})[1]
	got, err := search.Execute(context.Background(), rawArgs(t, map[string]any{"query": "example query"}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var results []searchResult
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, got)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}
	if results[0].Title != "Example Domain One" || results[0].URL != "https://example.com/page1" || results[0].Snippet != "First snippet & more." {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].URL != "https://example.org/page2" {
		t.Errorf("unexpected second result: %+v", results[1])
	}
}

func TestSearch_RespectsMaxResults(t *testing.T) {
	var page strings.Builder
	for i := 0; i < 5; i++ {
		page.WriteString(`<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F` +
			string(rune('a'+i)) + `">Result</a><a class="result__snippet">snip</a>`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(page.String()))
	}))
	defer server.Close()

	search := New(Config{SearchBaseURL: server.URL})[1]
	got, err := search.Execute(context.Background(), rawArgs(t, map[string]any{"query": "q", "max_results": 2}), agent.InvocationMeta{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var results []searchResult
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestSearch_RejectsEmptyQuery(t *testing.T) {
	search := New(Config{})[1]
	_, err := search.Execute(context.Background(), rawArgs(t, map[string]any{"query": "  "}), agent.InvocationMeta{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}
