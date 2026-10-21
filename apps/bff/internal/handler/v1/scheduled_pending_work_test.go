package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/annotation"
	conceptcache "github.com/rayer/llm-wiki-bff/internal/cache"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localfs"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/sourcestatus"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func TestScheduledPendingWorkClassifiesRawAnnotationBacklogAndNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	writeAnnotationFixture(t, root, "u", "p")
	rootStore := localfs.New(root)
	projectStore := rootStore.WithScope("u", "p")
	h := New(rootStore, nil, search.NewIndex(), conceptcache.New(), nil, nil)
	getCounts := func() (int, int, int) {
		t.Helper()
		newRaw, rawDirty, annotationDirty, err := h.pendingWorkForProject(ctx, "u", "p")
		if err != nil {
			t.Fatalf("evaluate pending source work: %v", err)
		}
		return newRaw, rawDirty, annotationDirty
	}
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 1 || rawDirty != 0 || annotationDirty != 0 {
		t.Fatalf("never-published raw counts=%d/%d/%d; want new=1, dirty=0", newRaw, rawDirty, annotationDirty)
	}
	seedSuccessfulReceipt := func() {
		t.Helper()
		rawFiles, err := projectStore.ListRawFiles(ctx)
		if err != nil {
			t.Fatalf("list raw inputs for receipt: %v", err)
		}
		var raw store.RawFile
		for _, item := range rawFiles {
			if item.Path == "raw/source.md" {
				raw = item
				break
			}
		}
		if raw.Path == "" {
			t.Fatal("source raw missing while creating receipt")
		}
		annotationDigest := annotation.Digest("")
		annotationMeta, err := projectStore.ListObjectMeta(ctx, "cache/annotations/")
		if err != nil {
			t.Fatalf("list annotation metadata for receipt: %v", err)
		}
		for _, item := range annotationMeta {
			if item.Path == "cache/annotations/s1.json" {
				annotationDigest = item.SHA256
			}
		}
		receipt := sourcestatus.Receipt{
			RawPath: "raw/source.md", LastIngestedRawSHA256: raw.SHA256,
			LastIngestedAnnSHA256: annotationDigest,
			LastIngestFingerprint: sourcestatus.Fingerprint(raw.SHA256, annotationDigest),
			LastSuccessAt:         time.Now().UTC().Format(time.RFC3339),
		}
		data, err := json.Marshal(sourcestatus.Artifact{Version: 1, Sources: map[string]sourcestatus.Receipt{"s1": receipt}})
		if err != nil {
			t.Fatalf("marshal successful source receipt: %v", err)
		}
		if _, err := projectStore.WriteBytes(ctx, data, sourcestatus.Path); err != nil {
			t.Fatalf("write successful source receipt: %v", err)
		}
	}
	if _, rawDirty, annotationDirty := getCounts(); rawDirty != 0 || annotationDirty != 0 {
		t.Fatalf("initial no-receipt evaluation unexpectedly classified dirty work: %d/%d", rawDirty, annotationDirty)
	}
	seedSuccessfulReceipt()
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 0 || rawDirty != 0 || annotationDirty != 0 {
		t.Fatalf("current consumed fingerprints counts=%d/%d/%d; want no work", newRaw, rawDirty, annotationDirty)
	}
	putAnnotation := func(body, generation string) handler.AnnotationResponse {
		t.Helper()
		payload, _ := json.Marshal(handler.AnnotationRequest{Body: body, ExpectedGeneration: generation})
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/sources/s1/annotation", bytes.NewReader(payload))
		c.Params = gin.Params{{Key: "id", Value: "s1"}}
		c.Set("userID", "u")
		c.Set("projectID", "p")
		h.PutAnnotation(c)
		var response handler.AnnotationResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("save annotation status=%d body=%s err=%v", recorder.Code, recorder.Body.String(), err)
		}
		return response
	}
	firstAnnotation := putAnnotation("guide one", "0")
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 0 || rawDirty != 0 || annotationDirty != 1 {
		t.Fatalf("annotation-only change counts=%d/%d/%d; want annotation=1", newRaw, rawDirty, annotationDirty)
	}
	if _, err := projectStore.WriteBytes(ctx, []byte("raw version two\n"), "raw/source.md"); err != nil {
		t.Fatalf("change raw input: %v", err)
	}
	secondAnnotation := putAnnotation("guide two", firstAnnotation.Generation)
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 0 || rawDirty != 1 || annotationDirty != 0 {
		t.Fatalf("combined raw and annotation change counts=%d/%d/%d; want raw=1, annotation=0", newRaw, rawDirty, annotationDirty)
	}
	seedSuccessfulReceipt()
	if _, err := projectStore.WriteBytes(ctx, []byte("raw version two\n"), "raw/source.md"); err != nil {
		t.Fatalf("save identical raw content: %v", err)
	}
	noopAnnotation := putAnnotation("guide two", secondAnnotation.Generation)
	if noopAnnotation.Generation != secondAnnotation.Generation {
		t.Fatalf("identical annotation save changed generation %q -> %q", secondAnnotation.Generation, noopAnnotation.Generation)
	}
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 0 || rawDirty != 0 || annotationDirty != 0 {
		t.Fatalf("same-content raw/annotation save counts=%d/%d/%d; want no work", newRaw, rawDirty, annotationDirty)
	}
	backlogPath := filepath.Join(root, "users", "u", "projects", "p", "raw", "backlog.md")
	if err := os.WriteFile(backlogPath, []byte("older than a day\n"), 0o644); err != nil {
		t.Fatalf("write prior-day backlog raw: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(backlogPath, old, old); err != nil {
		t.Fatalf("age backlog fixture: %v", err)
	}
	if newRaw, rawDirty, annotationDirty := getCounts(); newRaw != 1 || rawDirty != 0 || annotationDirty != 0 {
		t.Fatalf("prior-day backlog counts=%d/%d/%d; want new=1", newRaw, rawDirty, annotationDirty)
	}
}
