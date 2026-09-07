package v1

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
)

func TestStatusEndpointsEmptyGCSProject(t *testing.T) {
	for _, artifactStatus := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(artifactStatus), func(t *testing.T) {
			// Exercise the real GCS adapter: absent objects return 404, while
			// listing an empty project succeeds. No credentials or provider calls.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected storage mutation: %s", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/storage/v1/b/empty-project-test/o" {
					if !strings.HasPrefix(r.URL.Query().Get("prefix"), "users/request-user/projects/demo-project/") {
						t.Errorf("unscoped list: %s", r.URL)
					}
					fmt.Fprint(w, `{"items":[]}`)
					return
				}
				if !strings.Contains(r.URL.Path, "users/request-user/projects/demo-project/") {
					t.Errorf("unscoped read: %s", r.URL)
				}
				code := http.StatusNotFound
				if strings.HasSuffix(r.URL.Path, suggestedqueries.Path) {
					code = artifactStatus
				}
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"error":{"code":%d,"message":"test object unavailable"}}`, code)
			}))
			defer server.Close()
			t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
			root, err := gcs.NewClient("empty-project-test")
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			h := newStatusHandlerForTest(root, `{"executions":[]}`)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("userID", "request-user")
				c.Set("projectID", "demo-project")
			})
			router.GET("/api/v1/sources", h.ListSources)
			router.GET("/api/v1/concepts", h.ListConcepts)
			router.GET("/api/v1/status", h.Status)
			router.GET("/api/v1/pipeline/status", h.PipelineStatus)
			for _, endpoint := range []string{"/api/v1/sources", "/api/v1/concepts", "/api/v1/status", "/api/v1/pipeline/status"} {
				t.Run(endpoint, func(t *testing.T) {
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint, nil))
					want := http.StatusOK
					isStatus := strings.HasSuffix(endpoint, "/status")
					if isStatus && artifactStatus == http.StatusForbidden {
						want = http.StatusInternalServerError
					}
					if response.Code != want {
						t.Fatalf("status = %d, want %d; body = %s", response.Code, want, response.Body.String())
					}
					if isStatus && want == http.StatusOK && !strings.Contains(response.Body.String(), `"suggested_queries":[]`) {
						t.Fatalf("missing empty suggestions: %s", response.Body.String())
					}
				})
			}
		})
	}
}
