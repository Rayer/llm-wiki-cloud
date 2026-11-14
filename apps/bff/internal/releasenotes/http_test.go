package releasenotes

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicRoutesAreNoStoreAndDistinguishCurrentAndHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime := testRuntime()
	data := indexedStore(t, runtime, true)
	reader := NewHTTPHandler(data, runtime)
	router := gin.New()
	reader.Register(router)
	indexRequest := httptest.NewRequest(http.MethodGet, "/api/v1/public/release-notes", nil)
	indexResponse := httptest.NewRecorder()
	router.ServeHTTP(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK || indexResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index response status=%d cache=%q body=%s", indexResponse.Code, indexResponse.Header().Get("Cache-Control"), indexResponse.Body.String())
	}
	var index IndexResponse
	if err := json.Unmarshal(indexResponse.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if index.CurrentStatus != "available" || index.CurrentProductVersion != runtime.ProductVersion || index.Current == nil {
		t.Fatalf("index payload=%+v", index)
	}
	contentResponse := httptest.NewRecorder()
	router.ServeHTTP(contentResponse, httptest.NewRequest(http.MethodGet, "/api/v1/public/release-notes/2026.11.4?locale=fr", nil))
	if contentResponse.Code != http.StatusBadRequest || contentResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsupported locale status=%d body=%s", contentResponse.Code, contentResponse.Body.String())
	}
	historyResponse := httptest.NewRecorder()
	router.ServeHTTP(historyResponse, httptest.NewRequest(http.MethodGet, "/api/v1/public/release-notes/1.2.3?locale=en", nil))
	if historyResponse.Code != http.StatusNotFound {
		t.Fatalf("unindexed exact version status=%d", historyResponse.Code)
	}
}

func TestPublicReaderUnavailableOnStoreOutageAndAllowsSameVersionFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime := testRuntime()
	data := indexedStore(t, runtime, true)
	var index Index
	if err := json.Unmarshal(data.index, &index); err != nil {
		t.Fatal(err)
	}
	row := &index.Versions[0]
	zh := row.Locales["zh-TW"]
	delete(row.Locales, "zh-TW")
	data.index, _ = json.Marshal(index)
	router := gin.New()
	NewHTTPHandler(data, runtime).Register(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/public/release-notes/2026.11.4?locale=zh-TW", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("fallback status=%d body=%s", response.Code, response.Body.String())
	}
	var content ContentResponse
	if err := json.Unmarshal(response.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if !content.LocaleFallback || content.Locale != "en" || content.RequestedLocale != "zh-TW" || !content.IsCurrent {
		t.Fatalf("fallback payload=%+v (removed zh file %s)", content, zh.ObjectPath)
	}
	data.err = errors.New("offline")
	outage := httptest.NewRecorder()
	router.ServeHTTP(outage, httptest.NewRequest(http.MethodGet, "/api/v1/public/release-notes", nil))
	if outage.Code != http.StatusServiceUnavailable || outage.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("outage status=%d cache=%q", outage.Code, outage.Header().Get("Cache-Control"))
	}
}
