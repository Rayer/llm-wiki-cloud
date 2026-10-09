package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/config"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/query"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/syssettings"
)

const lwc336HermesHarnessEnabled = "LWC336_HERMES_HARNESS"

type lwc336HarnessExecutor struct{}

type lwc336MCPObservation struct {
	Count  uint64 `json:"count"`
	Method string `json:"method"`
	Status int    `json:"status"`
}

func (lwc336HarnessExecutor) Execute(_ context.Context, reader cache.Reader, request query.Request) (query.Result, error) {
	if reader == nil {
		return query.Result{}, fmt.Errorf("synthetic internal storage credential unavailable")
	}
	base := query.Result{Query: request.Query, Mode: request.Mode, Results: []search.Result{}}
	switch request.Query {
	case "executor-failure":
		return query.Result{}, fmt.Errorf("synthetic internal provider credential must not be exposed")
	case "empty":
		return base, nil
	case "insufficient":
		base.Status = "insufficient_evidence"
		base.Reason = "no_qualified_evidence"
		return base, nil
	case "model-prior":
		base.Status = "insufficient_evidence"
		base.Reason = "no_qualified_evidence"
		base.AISynth = "Synthetic model-prior answer; no provider request was made."
		base.AnswerBasis = "model_prior"
		base.WikiEvidenceStatus = "no_relevant_evidence"
		base.DisclosureRequired = true
		base.Citations = []search.Citation{}
		return base, nil
	default:
		base.Results = []search.Result{{ID: "synthetic-result-id", Title: "Synthetic Query result for " + reader.Prefix()}}
		if request.Mode == "full" {
			base.AISynth = "Synthetic answer from the controlled executor."
			base.Citations = []search.Citation{{ID: "synthetic-result-id", Text: "Synthetic Query result"}}
		}
		return base, nil
	}
}

// TestLWC336HermesServe is invoked only by docs/lwc-336/harness/run.sh. It
// serves the real production router and project-key auth on the Firestore
// emulator, then exposes one synthetic key over stdout's one-way pipe. It
// never logs or persists that response body or either generated secret.
func TestLWC336HermesServe(t *testing.T) {
	if os.Getenv(lwc336HermesHarnessEnabled) != "1" {
		t.Skip("owned native Hermes harness only")
	}
	if !loopbackFirestoreEmulator(t) {
		t.Fatal("native Hermes harness requires an explicit loopback Firestore emulator")
	}
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_CLOUD_SCOPE", "lwc336_native_hermes")
	projectName := fmt.Sprintf("lwc336-hermes-%d", time.Now().UnixNano())
	fsClient, err := firestoreClientForHermesHarness(projectName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsClient.Raw().Close() })
	ctx := context.Background()
	userID := fmt.Sprintf("lwc336owner%d", time.Now().UnixNano())
	projectID := "research"
	secondProjectID := "archive"
	if _, err := scopedfirestore.Collection(fsClient.Raw(), "users").Doc(userID).Set(ctx, map[string]any{
		"status": auth.AccountActive, "auth_version": int64(0), "role": "member",
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{projectID, secondProjectID} {
		if _, err := scopedfirestore.Collection(fsClient.Raw(), "projects").Doc(userID+"_"+id).Set(ctx, map[string]any{
			"user_id": userID, "project_id": id, "name": id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	const jwtSecret = "lwc336-hermes-local-router-secret"
	h := handlerv1.New(localfs.New(t.TempDir()), fsClient, nil, nil, nil, nil)
	h.SetQueryExecutor(lwc336HarnessExecutor{})
	router := newProductionRouter(config.Config{
		JWTSecret: jwtSecret, AuthSessionEnvironment: "lwc336-native-hermes",
		AllowedOrigins: []string{"http://127.0.0.1"},
	}, false, nil, fsClient, h, &syssettings.FakeStore{Enabled: true}, nil)
	webToken, err := auth.GenerateAccessToken(userID, "member", jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	create := func(project, name string) (string, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+project+"/keys", strings.NewReader(`{"name":"`+name+`"}`))
		request.Header.Set("Authorization", "Bearer "+webToken)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("synthetic key create status=%d", recorder.Code)
		}
		var response struct {
			Key struct {
				KeyID string `json:"key_id"`
			} `json:"key"`
			Secret string `json:"secret"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Key.KeyID == "" || response.Secret == "" {
			t.Fatal("synthetic project-key create omitted its in-memory response")
		}
		return response.Key.KeyID, response.Secret
	}
	keyID, key := create(projectID, "Hermes harness")
	_, secondKey := create(secondProjectID, "Cross-key harness")
	const observationPath = "/__lwc336_harness/mcp-observations"
	var observationMu sync.Mutex
	var observation lwc336MCPObservation
	// Keep only method/status/count so the Python probe can prove a revoked
	// native call reached the BFF without capturing headers, credentials, or bodies.
	harnessHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == observationPath {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			observationMu.Lock()
			snapshot := observation
			observationMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, r)
		if r.URL.Path == "/mcp" {
			observationMu.Lock()
			observation.Count++
			observation.Method = r.Method
			observation.Status = recorder.Code
			observationMu.Unlock()
		}
		for name, values := range recorder.Header() {
			w.Header()[name] = append([]string(nil), values...)
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
	server := httptest.NewServer(harnessHandler)
	t.Cleanup(server.Close)
	ready, err := json.Marshal(map[string]string{
		"url": server.URL + "/mcp", "key": key, "second_key": secondKey,
		"key_id": keyID, "user_id": userID, "project_id": projectID,
		"second_project_id": secondProjectID, "web_token": webToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "LWC336_READY %s\n", ready); err != nil {
		t.Fatal(err)
	}
	// The Python controller closes its pipe only after all transport and API
	// probes finish. This keeps the router/emulator alive for the whole run.
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func firestoreClientForHermesHarness(project string) (*scopedfirestore.Client, error) {
	return scopedfirestore.NewClientWithDatabase(project, "", "", "")
}
