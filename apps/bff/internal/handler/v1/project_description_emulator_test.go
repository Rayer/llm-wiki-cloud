package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
)

type projectDescriptionPipelineSpy struct {
	starts   atomic.Int32
	statuses atomic.Int32
	running  atomic.Int32
}

func (p *projectDescriptionPipelineSpy) Start(context.Context, string, string, string, bool, string) (string, error) {
	p.starts.Add(1)
	return "unexpected", nil
}

func (p *projectDescriptionPipelineSpy) Status(context.Context, string, string, string) (*handlerapi.PipelineExecutionResponse, error) {
	p.statuses.Add(1)
	return nil, nil
}

func (p *projectDescriptionPipelineSpy) Running(context.Context, string, string) (bool, error) {
	p.running.Add(1)
	return false, nil
}

func TestProjectDescriptionFirestoreEndToEnd(t *testing.T) {
	client := newOnboardingFirestore(t)
	h := New(nil, client, nil, nil, nil, nil)
	pipeline := &projectDescriptionPipelineSpy{}
	h.SetLocalPipelineExecutor(pipeline)
	userID := fmt.Sprintf("description-owner-%d", time.Now().UnixNano())
	key := fmt.Sprintf("description-key-%d", time.Now().UnixNano())

	created := invokeInitProject(t, h, userID, key, `{"name":"Wiki","project":"Wiki","project_name":"Wiki","description":"first\r\nsecond"}`)
	if created.Code != http.StatusAccepted {
		t.Fatalf("init status=%d body=%s", created.Code, created.Body.String())
	}
	var createResponse initProjectResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatal(err)
	}
	if createResponse.Name != "Wiki" || createResponse.Description != "first\nsecond" || createResponse.DescriptionRevision != 1 {
		t.Fatalf("created response = %+v", createResponse)
	}
	projectID := createResponse.ProjectID
	projectRef := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(userID, projectID))
	projectSnapshot, err := projectRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projectData := projectSnapshot.Data()
	if projectData["description"] != "first\nsecond" || projectData["description_revision"] != int64(1) {
		t.Fatalf("stored initial description = %v, revision=%v", projectData["description"], projectData["description_revision"])
	}
	if updatedAt, ok := projectData["description_updated_at"].(time.Time); !ok || updatedAt.Location() != time.UTC {
		t.Fatalf("initial description timestamp = %T(%v), want UTC time", projectData["description_updated_at"], projectData["description_updated_at"])
	}

	listed := invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects", userID, "", "", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0]["id"] != projectID || list[0]["description"] != "first\nsecond" || list[0]["description_revision"] != float64(1) {
		t.Fatalf("project list = %#v", list)
	}

	skipped := invokeInitProject(t, h, userID, "", `{"name":"No description"}`)
	if skipped.Code != http.StatusAccepted {
		t.Fatalf("init without description status=%d body=%s", skipped.Code, skipped.Body.String())
	}
	var skippedResponse initProjectResponse
	if err := json.Unmarshal(skipped.Body.Bytes(), &skippedResponse); err != nil {
		t.Fatal(err)
	}
	if skippedResponse.Description != "" || skippedResponse.DescriptionRevision != 1 {
		t.Fatalf("skipped description response = %+v", skippedResponse)
	}
	skippedSnapshot, err := scopedfirestore.Collection(client.Raw(), "projects").Doc(projectDocID(userID, skippedResponse.ProjectID)).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if skippedSnapshot.Data()["description"] != "" || skippedSnapshot.Data()["description_revision"] != int64(1) {
		t.Fatalf("skipped description storage = %v", skippedSnapshot.Data())
	}

	read := invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"revision":1`) {
		t.Fatalf("GET description status=%d body=%s", read.Code, read.Body.String())
	}
	updated := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(`{"description":"edited\nline","revision":1}`))
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"revision":2`) {
		t.Fatalf("PUT description status=%d body=%s", updated.Code, updated.Body.String())
	}

	if renamed := invokeProjectHandler(t, h, http.MethodPatch, "/api/v1/projects/"+projectID, userID, "", "", strings.NewReader(`{"name":"Renamed"}`)); renamed.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	projectSnapshot, err = projectRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projectData = projectSnapshot.Data()
	if projectData["name"] != "Renamed" || projectData["description"] != "edited\nline" || projectData["description_revision"] != int64(2) {
		t.Fatalf("rename changed description fields: %v", projectData)
	}

	stale := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(`{"description":"stale","revision":1}`))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s, want 409", stale.Code, stale.Body.String())
	}
	if retry := invokeInitProject(t, h, userID, key, `{"name":"Changed","project":"Changed","project_name":"Changed","description":"must not overwrite"}`); retry.Code != http.StatusAccepted {
		t.Fatalf("idempotent retry status=%d body=%s", retry.Code, retry.Body.String())
	} else {
		var retryResponse initProjectResponse
		if err := json.Unmarshal(retry.Body.Bytes(), &retryResponse); err != nil {
			t.Fatal(err)
		}
		if retryResponse.ProjectID != projectID || retryResponse.Name != "Wiki" || retryResponse.Description != "first\nsecond" {
			t.Fatalf("idempotent retry response = %+v", retryResponse)
		}
	}
	read = invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, nil)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"description":"edited\nline"`) {
		t.Fatalf("idempotent retry overwrote description: status=%d body=%s", read.Code, read.Body.String())
	}

	cleared := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(`{"description":"","revision":2}`))
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"revision":3`) {
		t.Fatalf("clear status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	for _, tc := range []struct {
		text   string
		status int
	}{
		{text: strings.Repeat("😀", 1000), status: http.StatusOK},
		{text: strings.Repeat("😀", 1001), status: http.StatusBadRequest},
	} {
		current, err := projectRef.Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		revision := current.Data()["description_revision"].(int64)
		body, _ := json.Marshal(projectDescriptionRequest{Description: &tc.text, Revision: &revision})
		response := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(string(body)))
		if response.Code != tc.status {
			t.Fatalf("%d-code-point update status=%d, want %d body=%s", len([]rune(tc.text)), response.Code, tc.status, response.Body.String())
		}
	}
	current, err := projectRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Data()["description"] != strings.Repeat("😀", 1000) || current.Data()["description_revision"] != int64(4) {
		t.Fatalf("1001-code-point rejection changed saved value: %v", current.Data())
	}
	unchanged := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(`{"description":"`+strings.Repeat("😀", 1000)+`","revision":4}`))
	if unchanged.Code != http.StatusOK || !strings.Contains(unchanged.Body.String(), `"revision":4`) {
		t.Fatalf("same-value save status=%d body=%s", unchanged.Code, unchanged.Body.String())
	}
	invalidUTF8Body := append([]byte(`{"description":"`), 0xff)
	invalidUTF8Body = append(invalidUTF8Body, []byte(`","revision":4}`)...)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{body: `{"description":"\u0000","revision":4}`, status: http.StatusBadRequest},
		{body: `{"description":"\uD800","revision":4}`, status: http.StatusBadRequest},
		{body: string(invalidUTF8Body), status: http.StatusBadRequest},
		{body: `{"description":"` + strings.Repeat("x", maxProjectDescriptionBytes) + `","revision":4}`, status: http.StatusRequestEntityTooLarge},
	} {
		response := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(tc.body))
		if response.Code != tc.status {
			t.Fatalf("invalid description body response status=%d, want %d body=%s", response.Code, tc.status, response.Body.String())
		}
	}

	otherOwner := invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/description", "other-owner", projectID, projectID, nil)
	if otherOwner.Code != http.StatusNotFound {
		t.Fatalf("cross-owner read status=%d body=%s", otherOwner.Code, otherOwner.Body.String())
	}
	marker := invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects/"+key+"/description", userID, key, key, nil)
	if marker.Code != http.StatusNotFound {
		t.Fatalf("idempotency marker read status=%d body=%s", marker.Code, marker.Body.String())
	}
	mismatched := invokeProjectHandler(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/description", userID, "different", "different", nil)
	if mismatched.Code != http.StatusBadRequest {
		t.Fatalf("mismatched target status=%d body=%s", mismatched.Code, mismatched.Body.String())
	}

	current, err = projectRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	baseRevision := current.Data()["description_revision"].(int64)
	results := make(chan int, 3)
	var wait sync.WaitGroup
	for _, text := range []string{"first concurrent edit", "second concurrent edit"} {
		wait.Add(1)
		go func(text string) {
			defer wait.Done()
			body, _ := json.Marshal(projectDescriptionRequest{Description: &text, Revision: &baseRevision})
			response := invokeProjectHandler(t, h, http.MethodPut, "/api/v1/projects/"+projectID+"/description", userID, projectID, projectID, strings.NewReader(string(body)))
			results <- response.Code
		}(text)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		response := invokeProjectHandler(t, h, http.MethodPatch, "/api/v1/projects/"+projectID, userID, "", "", strings.NewReader(`{"name":"Renamed concurrently"}`))
		results <- response.Code
	}()
	wait.Wait()
	close(results)
	statuses := map[int]int{}
	for code := range results {
		statuses[code]++
	}
	if statuses[http.StatusOK] != 2 || statuses[http.StatusConflict] != 1 {
		t.Fatalf("concurrent update statuses=%v, want two 200 and one 409", statuses)
	}
	current, err = projectRef.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Data()["name"] != "Renamed concurrently" {
		t.Fatalf("concurrent description save overwrote rename: %v", current.Data()["name"])
	}
	if pipeline.starts.Load() != 0 || pipeline.statuses.Load() != 0 || pipeline.running.Load() != 0 {
		t.Fatalf("project description actions touched Pipeline: starts=%d statuses=%d running=%d", pipeline.starts.Load(), pipeline.statuses.Load(), pipeline.running.Load())
	}
}

func invokeInitProject(t *testing.T, h *Handler, userID, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	return invokeProjectHandler(t, h, http.MethodPost, "/api/v1/init-project", userID, "", "", strings.NewReader(body), key)
}

func invokeProjectHandler(t *testing.T, h *Handler, method, path, userID, projectContext, headerProject string, body io.Reader, idempotencyKey ...string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, body)
	c.Set("userID", userID)
	if projectContext != "" {
		c.Set("projectID", projectContext)
		c.Request.Header.Set("X-Project-ID", headerProject)
	}
	if len(idempotencyKey) > 0 {
		c.Request.Header.Set("Idempotency-Key", idempotencyKey[0])
	}
	if strings.Contains(path, "/description") {
		c.Params = []gin.Param{{Key: "pid", Value: strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")[0]}}
		switch method {
		case http.MethodGet:
			h.GetProjectDescription(c)
		case http.MethodPut:
			h.PutProjectDescription(c)
		}
	} else {
		switch {
		case method == http.MethodPost && path == "/api/v1/init-project":
			h.InitProject(c)
		case method == http.MethodGet && path == "/api/v1/projects":
			h.ListProjects(c)
		case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/projects/"):
			c.Params = []gin.Param{{Key: "projectID", Value: strings.TrimPrefix(path, "/api/v1/projects/")}}
			h.RenameProject(c)
		default:
			t.Fatalf("unsupported handler test request %s %s", method, path)
		}
	}
	return recorder
}
