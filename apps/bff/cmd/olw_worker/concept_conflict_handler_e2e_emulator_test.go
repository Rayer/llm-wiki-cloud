//go:build lwc_local_synto_runtime && byokprofilefixture

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
)

type conceptConflictDispatchFixture struct {
	starts    int
	userID    string
	projectID string
	stage     string
	env       map[string]string
}

func (p *conceptConflictDispatchFixture) Start(context.Context, string, string, string, bool, string) (string, error) {
	return "", fmt.Errorf("unexpected legacy pipeline start")
}

func (p *conceptConflictDispatchFixture) StartWithAttemptAndEnvironment(
	_ context.Context,
	userID, projectID, stage string,
	_ bool,
	_, _ string,
	extraEnv map[string]string,
) (string, error) {
	p.starts++
	p.userID, p.projectID, p.stage = userID, projectID, stage
	p.env = make(map[string]string, len(extraEnv))
	for key, value := range extraEnv {
		p.env[key] = value
	}
	return fmt.Sprintf("lwc341-dispatch-%d", p.starts), nil
}

func (*conceptConflictDispatchFixture) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	return nil, nil
}

func (*conceptConflictDispatchFixture) Running(context.Context, string, string) (bool, error) {
	return false, nil
}

func requireConceptConflictFirestoreEmulator(t *testing.T) {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	host, port, err := net.SplitHostPort(endpoint)
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if err != nil || port == "" || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
		t.Fatalf("concept conflict E2E requires a loopback Firestore emulator, got %q", endpoint)
	}
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		t.Fatalf("configured loopback Firestore emulator is unavailable: %v", err)
	}
	_ = conn.Close()
}

func conceptConflictE2EContext(h *handlerv1.Handler, method, target string, body string, userID, projectID string) (*httptest.ResponseRecorder, *gin.Context) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ginContext.Set("userID", userID)
	ginContext.Set("projectID", projectID)
	return recorder, ginContext
}

func TestConceptConflictHandlerDispatchWorkerSnapshotReceiptAndRevokedReplay(t *testing.T) {
	requireConceptConflictFirestoreEmulator(t)
	const firestoreProject = "lwc341-disposable"
	const userID = "fixture-user"
	const projectID = "fixture-project"
	scope := fmt.Sprintf("lwc341-e2e-%d", time.Now().UnixNano())
	t.Setenv("GOOGLE_CLOUD_PROJECT", firestoreProject)
	t.Setenv("LOCAL_CLOUD_SCOPE", scope)
	client, err := internalfirestore.NewClientWithDatabaseAndScope(firestoreProject, firestore.DefaultDatabaseID, userID, projectID, scope)
	if err != nil {
		t.Fatalf("create scoped emulator Firestore client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projectRef := internalfirestore.Collection(client.Raw(), "projects").Doc(userID + "_" + projectID)
	if _, err := projectRef.Set(ctx, map[string]any{
		"user_id": userID, "project_id": projectID, "name": "LWC-341 disposable", "status": "ready",
	}); err != nil {
		_ = client.Close()
		t.Fatalf("seed disposable project authorization: %v", err)
	}
	t.Cleanup(func() {
		if _, err := projectRef.Delete(context.Background()); err != nil {
			t.Errorf("delete disposable project authorization: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("close scoped emulator Firestore client: %v", err)
		}
	})

	root := t.TempDir()
	vault := root + "/users/fixture-user/projects/fixture-project"
	operationFile := root + "/operation.json"
	localConflictFixture(t, "seed", vault, operationFile, "keep_published")
	dispatch := &conceptConflictDispatchFixture{}
	h := handlerv1.New(localfs.New(root), client, nil, nil, nil, nil)
	h.SetLocalPipelineExecutor(dispatch)
	h.SetAccountLookup(func(context.Context, string) (*auth.UserRecord, error) {
		return &auth.UserRecord{Status: auth.AccountActive, Role: "member"}, nil
	})
	gin.SetMode(gin.TestMode)

	compareRecorder, compareContext := conceptConflictE2EContext(h, http.MethodGet, "/api/v1/concept-conflicts/compare?path=wiki/A.md", "", userID, projectID)
	h.CompareConceptConflict(compareContext)
	if compareRecorder.Code != http.StatusOK {
		t.Fatalf("authenticated LocalFS compare status=%d body=%s", compareRecorder.Code, compareRecorder.Body.String())
	}
	var comparison struct {
		ProjectID   string `json:"project_id"`
		StorageView string `json:"storage_view"`
		PairSHA256  string `json:"pair_sha256"`
		ConflictID  string `json:"conflict_id"`
		Published   struct {
			Path string `json:"path"`
		} `json:"published"`
	}
	if err := json.Unmarshal(compareRecorder.Body.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.ProjectID != projectID || comparison.Published.Path != "wiki/A.md" ||
		!strings.HasPrefix(comparison.StorageView, "local-lease-") || comparison.PairSHA256 == "" || comparison.ConflictID == "" {
		t.Fatalf("LocalFS comparison did not pin one authenticated pair: %+v", comparison)
	}

	operationID := "operation-e2e-lwc341"
	resolvePayload, err := json.Marshal(struct {
		OperationID string `json:"operation_id"`
		Action      string `json:"action"`
		ConflictID  string `json:"conflict_id"`
		Path        string `json:"path"`
		StorageView string `json:"storage_view"`
		PairSHA256  string `json:"pair_sha256"`
	}{operationID, "keep_published", comparison.ConflictID, "wiki/A.md", comparison.StorageView, comparison.PairSHA256})
	if err != nil {
		t.Fatal(err)
	}
	resolveRecorder, resolveContext := conceptConflictE2EContext(h, http.MethodPost, "/api/v1/concept-conflicts/resolve", string(resolvePayload), userID, projectID)
	h.ResolveConceptConflict(resolveContext)
	if resolveRecorder.Code != http.StatusAccepted || dispatch.starts != 1 || dispatch.userID != userID ||
		dispatch.projectID != projectID || dispatch.stage != "full" || dispatch.env["LWC_CONCEPT_RESOLVE_JSON"] == "" ||
		dispatch.env["LWC_CONFLICT_STORAGE_VIEW"] != comparison.StorageView {
		t.Fatalf("Resolve did not dispatch trusted scoped worker input: status=%d body=%s dispatch=%+v", resolveRecorder.Code, resolveRecorder.Body.String(), dispatch)
	}
	var accepted struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal(resolveRecorder.Body.Bytes(), &accepted); err != nil || accepted.ExecutionID == "" {
		t.Fatalf("accepted execution response=%s decode=%v", resolveRecorder.Body.String(), err)
	}
	operationJSON := dispatch.env["LWC_CONCEPT_RESOLVE_JSON"]
	if err := os.WriteFile(operationFile, []byte(operationJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := conceptConflictWorkerConfig(t, root, operationFile)
	cfg.ConceptResolveJSON = operationJSON
	cfg.ConceptConflictSourceVault = vault
	cfg.ExecutionID = accepted.ExecutionID
	if err := runLocalConflictWorker(t, vault, cfg); err != nil {
		t.Fatalf("actual Local worker dispatch failed: %v\n%s", err, localConflictLog(vault))
	}
	localConflictFixture(t, "verify", vault, operationFile)

	statusRecorder, statusContext := conceptConflictE2EContext(h, http.MethodGet,
		"/api/v1/concept-conflicts/operations/"+operationID, "", userID, projectID)
	statusContext.Params = gin.Params{{Key: "operationID", Value: operationID}}
	h.GetConceptConflictOperation(statusContext)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("receipt read from committed LocalFS snapshot status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
	var status struct {
		Status string         `json:"status"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil || status.Status != "committed" || status.Result["action"] != "keep_published" {
		t.Fatalf("committed receipt status=%+v decode=%v body=%s", status, err, statusRecorder.Body.String())
	}

	replayRecorder, replayContext := conceptConflictE2EContext(h, http.MethodPost, "/api/v1/concept-conflicts/resolve", string(resolvePayload), userID, projectID)
	h.ResolveConceptConflict(replayContext)
	var replay map[string]any
	if replayRecorder.Code != http.StatusOK || json.Unmarshal(replayRecorder.Body.Bytes(), &replay) != nil ||
		replay["status"] != "committed" || dispatch.starts != 1 {
		t.Fatalf("same-payload retry did not return original receipt: status=%d body=%s dispatches=%d", replayRecorder.Code, replayRecorder.Body.String(), dispatch.starts)
	}

	if _, err := projectRef.Delete(ctx); err != nil {
		t.Fatalf("revoke fixture project authorization: %v", err)
	}
	revokedReplayRecorder, revokedReplayContext := conceptConflictE2EContext(h, http.MethodPost, "/api/v1/concept-conflicts/resolve", string(resolvePayload), userID, projectID)
	h.ResolveConceptConflict(revokedReplayContext)
	if revokedReplayRecorder.Code != http.StatusNotFound || strings.Contains(revokedReplayRecorder.Body.String(), `"status":"committed"`) || dispatch.starts != 1 {
		t.Fatalf("revoked same-payload retry returned receipt or dispatched work: status=%d body=%s starts=%d", revokedReplayRecorder.Code, revokedReplayRecorder.Body.String(), dispatch.starts)
	}
	revokedStatusRecorder, revokedStatusContext := conceptConflictE2EContext(h, http.MethodGet,
		"/api/v1/concept-conflicts/operations/"+operationID, "", userID, projectID)
	revokedStatusContext.Params = gin.Params{{Key: "operationID", Value: operationID}}
	h.GetConceptConflictOperation(revokedStatusContext)
	if revokedStatusRecorder.Code != http.StatusNotFound || strings.Contains(revokedStatusRecorder.Body.String(), `"status":"committed"`) {
		t.Fatalf("revoked receipt read leaked result: status=%d body=%s", revokedStatusRecorder.Code, revokedStatusRecorder.Body.String())
	}
}
