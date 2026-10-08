package v1

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/pipelinequota"
)

type recompileAllQuotaSpy struct{ calls atomic.Int32 }

func (s *recompileAllQuotaSpy) LoadQuotaState(context.Context, string, string) (int, string, time.Time, error) {
	s.calls.Add(1)
	return 0, "", time.Time{}, nil
}

func (s *recompileAllQuotaSpy) ReserveQuota(context.Context, string, string, string, pipelinequota.Limits, time.Time, bool, bool, int, int, int) (pipelinequota.Snapshot, bool, error) {
	s.calls.Add(1)
	return pipelinequota.Snapshot{}, false, nil
}

func (s *recompileAllQuotaSpy) LinkQuotaReservation(context.Context, string, string) error {
	s.calls.Add(1)
	return nil
}

func (s *recompileAllQuotaSpy) GetQuotaReservation(context.Context, string) (internalfirestore.QuotaReservation, bool, error) {
	s.calls.Add(1)
	return internalfirestore.QuotaReservation{}, false, nil
}

func (s *recompileAllQuotaSpy) ListPendingQuotaReservations(context.Context) ([]internalfirestore.QuotaReservation, error) {
	s.calls.Add(1)
	return nil, nil
}

func (s *recompileAllQuotaSpy) SettleQuotaReservation(context.Context, string, string) (string, error) {
	s.calls.Add(1)
	return "not_applicable", nil
}

func TestRecompileAllCapabilityAndDenialAreScopedAndFailClosed(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	parsed, err := url.Parse("http://" + endpoint)
	if err != nil || parsed.Host == "" {
		t.Fatal("invalid Firestore emulator host")
	}
	if host, _, err := net.SplitHostPort(parsed.Host); err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatalf("Firestore emulator must use loopback, got %q", parsed.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	userID := "lwc353-recompile-owner"
	projectID := "p" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	db, err := internalfirestore.NewClient("lwc353-recompile-test", userID, projectID)
	if err != nil {
		t.Fatalf("create Firestore emulator client: %v", err)
	}
	defer db.Close()
	projectRef := db.Raw().Collection("projects").Doc(projectDocID(userID, projectID))
	if _, err := projectRef.Set(ctx, map[string]interface{}{"user_id": userID, "project_id": projectID, "status": "ready"}); err != nil {
		t.Fatalf("create Project metadata: %v", err)
	}

	gin.SetMode(gin.TestMode)
	h := New(nil, db, nil, nil, nil, nil)
	quota := &recompileAllQuotaSpy{}
	h.SetPipelineQuotaStore(quota)
	var jobCalls atomic.Int32
	jobServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		jobCalls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer jobServer.Close()
	h.SetPipelineJobURL(jobServer.URL)
	h.httpClient = jobServer.Client()

	router := gin.New()
	router.GET("/projects/:pid/recompile-all/capability", func(c *gin.Context) {
		if principal := c.GetHeader("X-Test-Principal"); principal != "" {
			c.Set("userID", principal)
		}
		h.RecompileAllCapability(c)
	})
	router.POST("/projects/:pid/recompile-all", func(c *gin.Context) {
		if principal := c.GetHeader("X-Test-Principal"); principal != "" {
			c.Set("userID", principal)
		}
		h.RecompileAll(c)
	})
	request := func(method, path, principal, header string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		if principal != "" {
			req.Header.Set("X-Test-Principal", principal)
		}
		if header != "" {
			req.Header.Set("X-Project-ID", header)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}

	capability := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", userID, projectID)
	if capability.Code != http.StatusOK {
		t.Fatalf("capability status=%d body=%s, want 200", capability.Code, capability.Body.String())
	}
	var gotCapability recompileAllCapabilityResponse
	if err := json.Unmarshal(capability.Body.Bytes(), &gotCapability); err != nil {
		t.Fatalf("decode capability: %v", err)
	}
	if gotCapability.Allowed || gotCapability.DenialCode != recompileAllDenialByokRequired {
		t.Fatalf("capability=%+v, want denied by %q", gotCapability, recompileAllDenialByokRequired)
	}

	denied := request(http.MethodPost, "/projects/"+projectID+"/recompile-all", userID, projectID)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("recompile status=%d body=%s, want 403", denied.Code, denied.Body.String())
	}
	var gotDenial recompileAllDenialResponse
	if err := json.Unmarshal(denied.Body.Bytes(), &gotDenial); err != nil {
		t.Fatalf("decode denial: %v", err)
	}
	if gotDenial.Error != recompileAllDenialByokRequired || gotDenial.DenialCode != recompileAllDenialByokRequired {
		t.Fatalf("denial=%+v, want explicit byok_required", gotDenial)
	}

	if calls := quota.calls.Load(); calls != 0 {
		t.Fatalf("recompile denial touched pipeline quota %d times", calls)
	}
	if calls := jobCalls.Load(); calls != 0 {
		t.Fatalf("recompile denial started Cloud Run %d times", calls)
	}

	unauthorized := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", "", projectID)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal status=%d body=%s, want 401", unauthorized.Code, unauthorized.Body.String())
	}

	mismatch := request(http.MethodPost, "/projects/"+projectID+"/recompile-all", userID, "other-project")
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("project header mismatch status=%d body=%s, want 400", mismatch.Code, mismatch.Body.String())
	}

	foreign := request(http.MethodGet, "/projects/"+projectID+"/recompile-all/capability", "lwc353-attacker", projectID)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign Project capability status=%d body=%s, want 404", foreign.Code, foreign.Body.String())
	}
}
