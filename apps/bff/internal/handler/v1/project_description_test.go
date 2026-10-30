package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNormalizeProjectDescriptionUsesUnicodeScalarsAndLF(t *testing.T) {
	value := "  first\r\nsecond\rthird  "
	got, err := normalizeProjectDescription(value)
	if err != nil || got != "  first\nsecond\nthird  " {
		t.Fatalf("normalizeProjectDescription = %q, %v", got, err)
	}
	if _, err := normalizeProjectDescription(strings.Repeat("😀", 1000)); err != nil {
		t.Fatalf("1000 supplementary code points rejected: %v", err)
	}
	for _, value := range []string{strings.Repeat("😀", 1001), "a\x00b", string([]byte{0xff})} {
		if _, err := normalizeProjectDescription(value); !errors.Is(err, errProjectDescriptionInvalid) {
			t.Fatalf("normalizeProjectDescription(%q) error=%v, want invalid", value, err)
		}
	}
}

func TestDecodeProjectDescriptionJSONStrictInput(t *testing.T) {
	for _, body := range []string{
		`{"description":"first","description":"second","revision":0}`,
		`{"description":"first","descripti\u006fn":"second","revision":0}`,
		`{"description":"ok","revision":0,"extra":true}`,
		`{"description":"ok","revision":0} {}`,
		`{"description":null,"revision":0}`,
		`{"description":"\uD800","revision":0}`,
		`{"description":"\uDC00","revision":0}`,
		`{"description":"\uD800x","revision":0}`,
	} {
		var request projectDescriptionRequest
		if err := decodeProjectDescriptionJSON(strings.NewReader(body), &request); err == nil {
			t.Errorf("decodeProjectDescriptionJSON accepted %s", body)
		}
	}

	var request projectDescriptionRequest
	if err := decodeProjectDescriptionJSON(strings.NewReader(`{"description":"\uD83D\uDE00","revision":0}`), &request); err != nil {
		t.Fatalf("decode valid surrogate pair: %v", err)
	}
	if request.Description == nil || *request.Description != "😀" || request.Revision == nil || *request.Revision != 0 {
		t.Fatalf("decoded request = %+v", request)
	}
	if err := decodeProjectDescriptionJSON(strings.NewReader(`{"description":"x","revision":0,"revision":1}`), &request); err == nil {
		t.Fatal("duplicate revision was accepted")
	}
	if err := decodeProjectDescriptionJSON(bytes.NewReader(append([]byte(`{"description":"`), append([]byte{0xff}, []byte(`","revision":0}`)...)...)), &request); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	if err := decodeProjectDescriptionJSON(strings.NewReader(`{"description":"x","revision":0}`+strings.Repeat(" ", maxProjectDescriptionBytes)), &request); !errors.Is(err, errProjectDescriptionTooLarge) {
		t.Fatalf("oversized request error=%v, want body limit", err)
	}
}

func TestOptionalInitProjectDescriptionPreservesAliasesAndValidatesText(t *testing.T) {
	var request initProjectRequest
	if err := decodeInitProjectJSON(strings.NewReader(`{"name":"Wiki","project":"Wiki","project_name":"Wiki"}`), &request); err != nil {
		t.Fatalf("legacy project aliases were rejected: %v", err)
	}
	if got, err := optionalInitProjectDescription(request.Description); err != nil || got != "" {
		t.Fatalf("omitted description = %q, %v", got, err)
	}
	if err := decodeInitProjectJSON(strings.NewReader(`{"name":"Wiki","description":"first\r\nsecond"}`), &request); err != nil {
		t.Fatal(err)
	}
	if got, err := optionalInitProjectDescription(request.Description); err != nil || got != "first\nsecond" {
		t.Fatalf("initial description = %q, %v", got, err)
	}
	for _, body := range []string{
		`{"name":"Wiki","description":null}`,
		`{"name":"Wiki","description":4}`,
		`{"name":"Wiki","description":"\uD800"}`,
		`{"name":"Wiki","description":"` + strings.Repeat("x", 1001) + `"}`,
	} {
		if err := decodeInitProjectJSON(strings.NewReader(body), &request); err != nil {
			if errors.Is(err, errProjectDescriptionInvalid) {
				continue
			}
			t.Fatalf("decode init body %q: %v", body, err)
		}
		if _, err := optionalInitProjectDescription(request.Description); !errors.Is(err, errProjectDescriptionInvalid) {
			t.Errorf("optionalInitProjectDescription accepted %q: %v", body, err)
		}
	}
	invalidUTF8 := json.RawMessage(append([]byte{'"'}, append([]byte{0xff}, '"')...))
	if _, err := optionalInitProjectDescription(invalidUTF8); !errors.Is(err, errProjectDescriptionInvalid) {
		t.Fatalf("optionalInitProjectDescription accepted invalid UTF-8: %v", err)
	}
}

func TestProjectResponseIncludesDescriptionProjection(t *testing.T) {
	response, owner, ok := projectResponseFromFirestoreDoc("owner_project", map[string]interface{}{
		"user_id":              "owner",
		"project_id":           "project",
		"name":                 "Project",
		"description":          "  purpose\n",
		"description_revision": int64(3),
	})
	if !ok || owner != "owner" || response.Description != "  purpose\n" || response.DescriptionRevision != 3 {
		t.Fatalf("project projection = %+v owner=%q ok=%v", response, owner, ok)
	}

	legacy, owner, ok := projectResponseFromFirestoreDoc("owner_old", map[string]interface{}{
		"user_id":    "owner",
		"project_id": "old",
		"name":       "Old",
	})
	if !ok || owner != "owner" || legacy.Description != "" || legacy.DescriptionRevision != 0 {
		t.Fatalf("legacy projection = %+v owner=%q ok=%v", legacy, owner, ok)
	}
}

func TestProjectDescriptionTargetRejectsURLHeaderMismatch(t *testing.T) {
	ginContext := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(ginContext)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/path/description", nil)
	c.Params = []gin.Param{{Key: "pid", Value: "path"}}
	c.Request.Header.Set("X-Project-ID", "different")
	if _, ok := projectDescriptionTarget(c); ok {
		t.Fatal("path/header Project mismatch was accepted")
	}
}
