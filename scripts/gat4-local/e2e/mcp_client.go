// mcp_client.go exercises the real Streamable HTTP MCP client against a local
// BFF. It reads its synthetic OAuth access token from a mode-0600 scratch file
// and never writes the token to stdout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerRoundTripper struct {
	base          http.RoundTripper
	token         string
	projectHeader string
	mu            sync.Mutex
	statuses      []int
}

func (t *bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	if t.projectHeader != "" {
		clone.Header.Set("X-Project-ID", t.projectHeader)
	}
	response, err := t.base.RoundTrip(clone)
	if response != nil {
		t.mu.Lock()
		t.statuses = append(t.statuses, response.StatusCode)
		t.mu.Unlock()
	}
	return response, err
}

func (t *bearerRoundTripper) observedStatus(statuses ...int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, actual := range t.statuses {
		for _, expected := range statuses {
			if actual == expected {
				return actual
			}
		}
	}
	return 0
}

func (t *bearerRoundTripper) statusCodes() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]int(nil), t.statuses...)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "http://localhost:62382/mcp/projects/default", "local project MCP resource endpoint")
	tokenFile := flag.String("token-file", "", "mode-0600 file containing the synthetic OAuth access token")
	expectRevoked := flag.Bool("expect-revoked", false, "expect the MCP client to reject this revoked access token")
	flag.Parse()
	if strings.TrimSpace(*tokenFile) == "" {
		return errors.New("--token-file is required")
	}
	info, err := os.Stat(*tokenFile)
	if err != nil {
		return fmt.Errorf("inspect token file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("token file must not be accessible by group or others")
	}
	data, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("token file does not contain one access token")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if *expectRevoked {
		observer := &bearerRoundTripper{base: http.DefaultTransport, token: token}
		_, err := connect(ctx, *endpoint, observer)
		status := observer.observedStatus(http.StatusUnauthorized, http.StatusForbidden)
		if err == nil || status == 0 {
			return fmt.Errorf("revoked token was not rejected with HTTP 401 or 403")
		}
		fmt.Printf("case=mcp_revoked_access_token result=%d_rejected\n", status)
		return nil
	}

	observer := &bearerRoundTripper{base: http.DefaultTransport, token: token}
	session, err := connect(ctx, *endpoint, observer)
	if err != nil {
		return fmt.Errorf("MCP Streamable HTTP initialize failed: %w", err)
	}
	defer session.Close()

	initialized := session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil {
		return errors.New("MCP server did not return initialization information")
	}
	var toolNames []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("MCP tools/list failed: %w", err)
		}
		toolNames = append(toolNames, tool.Name)
	}
	sort.Strings(toolNames)
	if len(toolNames) != 1 || toolNames[0] != "query_project" {
		return fmt.Errorf("query-only project token exposed unexpected tools: %s", strings.Join(toolNames, ","))
	}

	if _, err := expectDenied(ctx, *endpoint, token, "different-project", http.StatusForbidden); err != nil {
		return fmt.Errorf("cross-project header negative control: %w", err)
	}
	otherResource := strings.TrimSuffix(*endpoint, "/default") + "/gat4-unowned-negative"
	otherStatus, err := expectDenied(ctx, otherResource, token, "", http.StatusUnauthorized, http.StatusForbidden)
	if err != nil {
		return fmt.Errorf("cross-resource negative control: %w", err)
	}
	fmt.Printf("case=mcp_streamable_http_initialize result=pass server=%s tools=%s\n", initialized.ServerInfo.Name, strings.Join(toolNames, ","))
	fmt.Println("case=mcp_cross_project_header result=403_denied")
	fmt.Printf("case=mcp_other_project_resource result=%d_denied\n", otherStatus)
	return nil
}

func connect(ctx context.Context, endpoint string, roundTripper *bearerRoundTripper) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "gat4-local-qualification", Version: "1"}, nil)
	httpClient := &http.Client{
		Transport: roundTripper,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}
	return client.Connect(ctx, transport, nil)
}

func expectDenied(ctx context.Context, endpoint, token string, projectHeader string, wantStatuses ...int) (int, error) {
	observer := &bearerRoundTripper{base: http.DefaultTransport, token: token, projectHeader: projectHeader}
	session, err := connect(ctx, endpoint, observer)
	if session != nil {
		_ = session.Close()
	}
	status := observer.observedStatus(wantStatuses...)
	if err == nil || status == 0 {
		return 0, fmt.Errorf("expected HTTP %v, got success=%t statuses=%v", wantStatuses, err == nil, observer.statusCodes())
	}
	return status, nil
}
