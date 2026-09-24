package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	v1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

// Explicit opt-in browser harness: real production HTTP router/detail/synthesis,
// fixed retrieval selections and mock LLM transport; no external provider calls.
type browserCitationExecutor struct{ service *query.Service }

func (e browserCitationExecutor) Execute(ctx context.Context, reader cache.Reader, req query.Request) (query.Result, error) {
	return e.service.SynthesizeWithError(ctx, reader, req, query.Result{Results: []search.Result{
		{Slug: "台北 café", Title: "Taipei Concept", Type: "concept"},
		{Slug: "來源 café", Title: "Taipei Source", Type: "source"},
	}})
}

type browserCitationTransport struct{}

func (browserCitationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	refs := regexp.MustCompile(`\[CITATION_REF_[^\]]+\]`).FindAllString(string(data), -1)
	seen := map[string]bool{}
	content := "Browser citation evidence:"
	for _, ref := range refs {
		if !seen[ref] {
			content += " " + ref
			seen[ref] = true
		}
	}
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}
func TestLocalCitationBrowserServer(t *testing.T) {
	rootPath := os.Getenv("LWC_BROWSER_ROOT")
	if rootPath == "" {
		t.Skip("opt-in real browser harness")
	}
	addr := os.Getenv("LWC_BROWSER_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18081"
	}
	root := localfs.New(rootPath)
	reader := root.Scope("local-user", "citation-browser")
	files := map[string]string{
		"index.md":                "# Citation browser fixture\n",
		"cache/id_map.json":       `{"concept":{"01JAZ5N7Y3K8M2Q4R6T9VWXABC":"台北 café"},"source":{"abcdef123456":"來源 café"}}`,
		"cache/concepts.jsonl":    "{\"slug\":\"台北 café\",\"title\":\"Taipei Concept\",\"body\":\"Intended concept evidence body\"}\n",
		"wiki/台北 café.md":         "---\ntitle: Taipei Concept\nstatus: published\n---\nIntended concept evidence body\n",
		"wiki/sources/來源 café.md": "---\ntitle: Taipei Source\nstatus: published\n---\nIntended source evidence body\n",
	}
	for name, data := range files {
		if _, err := reader.WriteBytes(context.Background(), []byte(data), name); err != nil {
			t.Fatal(err)
		}
	}
	old := http.DefaultTransport
	http.DefaultTransport = browserCitationTransport{}
	defer func() { http.DefaultTransport = old }()
	cc := cache.New()
	h := v1.New(root, nil, search.NewIndex(), cc, nil, nil)
	h.SetQueryExecutor(browserCitationExecutor{query.NewService(cc, nil, llm.NewClient("explicit-mock-not-a-key"))})
	gin.SetMode(gin.ReleaseMode)
	router := newProductionRouter(config.Config{DevJWT: true, JWTSecret: "isolated-browser-fixture", AllowedOrigins: []string{"http://localhost:18080"}}, true, nil, nil, h, &syssettings.FakeStore{Enabled: true}, nil)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	if err := os.WriteFile(filepath.Join(rootPath, "ready"), []byte(addr), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser harness timeout")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(rootPath, "stop")); err == nil {
				return
			}
		}
	}
}
