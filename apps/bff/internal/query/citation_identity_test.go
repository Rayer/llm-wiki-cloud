package query

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/search"
	"github.com/rayer/llm-wiki-bff/internal/storage"
	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

func TestCitationIdentityUsesRequestMap(t *testing.T) {
	for _, id := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "abcdef123456"} {
		t.Run(id, func(t *testing.T) {
			service, reader := serviceFixture(t, `{"slug":"台北 coffee","title":"Display label","body":"Evidence"}`+"\n")
			_, err := reader.WriteBytes(context.Background(), []byte(`{"concept":{"`+id+`":"台北 coffee"}}`), "cache/id_map.json")
			if err != nil {
				t.Fatal(err)
			}
			old := http.DefaultTransport
			capture := &promptCaptureTransport{}
			http.DefaultTransport = capture
			t.Cleanup(func() { http.DefaultTransport = old })
			service.llm = llm.NewClient("explicit-mock")
			result, err := service.SynthesizeWithError(context.Background(), reader, Request{Query: "coffee", Mode: "wiki"}, Result{Results: []search.Result{{Slug: "台北 coffee", Title: "Display label", Type: "concept"}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Citations) != 1 || result.Citations[0].Path != "/concepts/"+id+"-%E5%8F%B0%E5%8C%97%20coffee" || result.Citations[0].Text != "Display label" {
				t.Fatalf("citations=%+v", result.Citations)
			}
			if !strings.Contains(capture.user, "[CITATION_REF_") || result.Results[0].Slug != "台北 coffee" {
				t.Fatal("identity resolution altered retrieval or failed issuance")
			}
		})
	}
}

func TestUnselectedPercentSourceDoesNotPoisonQuery(t *testing.T) {
	service, reader := serviceFixture(t, `{"slug":"coffee","title":"Coffee","body":"coffee body"}`+"\n")
	ids, err := LoadCitationIDMap(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if ids.Source == nil {
		ids.Source = map[string]string{}
	}
	ids.Source["3b4e9275d077"] = "CLAUDE.md這樣寫才對！12條規則一次整理，讓Claude Code錯誤率從41%降至3%"
	data, err := wikiindex.EncodeIDMap(ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.WriteBytes(context.Background(), data, wikiindex.IDMapPath); err != nil {
		t.Fatal(err)
	}

	transport := &queryLLMTransport{t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	service.llm = llm.NewClient("explicit-mock")
	result, err := service.Execute(context.Background(), reader, Request{Query: "coffee", Mode: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AISynth != "answer [Coffee]" || len(result.Citations) != 1 || result.Citations[0].Slug != "coffee" {
		t.Fatalf("unselected percent source poisoned query or citation: %#v", result)
	}
}

func TestSelectedPercentSourceIssuesEscapedCanonicalCitation(t *testing.T) {
	const slug = "CLAUDE.md這樣寫才對！12條規則一次整理，讓Claude Code錯誤率從41%降至3%"
	const id = "3b4e9275d077"
	service, reader := serviceFixture(t, "")
	ids := wikiindex.IDMap{Concept: map[string]string{}, Source: map[string]string{id: slug}, Redirects: map[string][]string{}}
	data, err := wikiindex.EncodeIDMap(ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.WriteBytes(context.Background(), data, wikiindex.IDMapPath); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.WriteBytes(context.Background(), []byte("Source evidence"), "wiki/sources/"+slug+".md"); err != nil {
		t.Fatal(err)
	}

	transport := &queryLLMTransport{t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	service.llm = llm.NewClient("explicit-mock")
	result, err := service.SynthesizeWithError(context.Background(), reader, Request{Query: "test", Mode: "wiki"}, Result{Results: []search.Result{{Slug: slug, Title: "Claude Code guide", Type: "source"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Citations) != 1 || result.Citations[0].ID != id || result.Citations[0].Slug != slug || result.Citations[0].Path != "/sources/"+id+"-"+url.PathEscape(slug) {
		t.Fatalf("citation = %#v, want canonical ID and escaped source route", result.Citations)
	}
	if !strings.Contains(result.Citations[0].Path, "41%25") || !strings.Contains(result.Citations[0].Path, "3%25") {
		t.Fatalf("literal percent was not escaped as one URL path segment: %q", result.Citations[0].Path)
	}
}

func TestCitationIdentityRejectsInvalidMapsAndRoutes(t *testing.T) {
	for _, tc := range []struct{ name, data, slug, kind string }{
		{"missing", `{}`, "café tea", "concept"},
		{"malformed", `{`, "café tea", "concept"},
		{"duplicate key", `{"concept":{"abcdef123456":"café tea","abcdef123456":"other"}}`, "café tea", "concept"},
		{"ambiguous", `{"concept":{"abcdef123456":"café tea","123456abcdef":"café tea"}}`, "café tea", "concept"},
		{"trimmed ID alias", `{"concept":{"abcdef123456":"café tea"," abcdef123456 ":"other"}}`, "café tea", "concept"},
		{"invalid ID", `{"concept":{"../bad":"café tea"}}`, "café tea", "concept"},
		{"cross type ID", `{"concept":{"abcdef123456":"café tea"},"source":{"abcdef123456":"other"}}`, "café tea", "concept"},
		{"wrong type", `{"source":{"abcdef123456":"café tea"}}`, "café tea", "concept"},
		{"no normalization", `{"concept":{"abcdef123456":"Café tea"}}`, "café tea", "concept"},
		{"legacy side map not identity", `{"concept_entity_id":{"abcdef123456":"01ARZ3NDEKTSV4RRFFQ69G5FAV"}}`, "café tea", "concept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reader := serviceFixture(t, "")
			if _, err := reader.WriteBytes(context.Background(), []byte(tc.data), "cache/id_map.json"); err != nil {
				t.Fatal(err)
			}
			ids, err := LoadCitationIDMap(context.Background(), reader)
			if err == nil {
				_, err = ResolveCitationIdentity(search.Result{Slug: tc.slug, Type: tc.kind}, ids)
			}
			if err == nil {
				t.Fatal("invalid map received citation authority")
			}
		})
	}
	for _, slug := range []string{"../escape", "a/b", `a\b`, "a\x00b", "a\nline", "a\tline", " leading", "trailing ", ".", "..", ""} {
		t.Run("route "+slug, func(t *testing.T) {
			ids := wikiindex.IDMap{Concept: map[string]string{"abcdef123456": slug}}
			if _, err := ResolveCitationIdentity(search.Result{Slug: slug, Type: "concept"}, ids); err == nil {
				t.Fatal("unsafe route accepted")
			}
		})
	}
}

func TestLiteralPercentEscapeTextIsNotDecodedAsPathTraversal(t *testing.T) {
	const slug = "guide-%2e%2e-%2F-stays-literal"
	const id = "abcdef123456"
	ids := wikiindex.IDMap{Concept: map[string]string{id: slug}}
	resolved, err := ResolveCitationIdentity(search.Result{Slug: slug, Type: "concept"}, ids)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := search.NewCitationAuthority([]search.Result{resolved})
	if err != nil {
		t.Fatal(err)
	}
	authority.AddContext(0, resolved, "body")
	citation := authority.IssuedCitations()[0]
	if citation.Path != "/concepts/"+id+"-guide-%252e%252e-%252F-stays-literal" {
		t.Fatalf("literal percent escape was not encoded exactly once: %q", citation.Path)
	}
	if citation.Slug != slug {
		t.Fatalf("canonical display slug changed: %q", citation.Slug)
	}
}

func TestCitationSourceAndConceptGenerationIsolation(t *testing.T) {
	old := http.DefaultTransport
	capture := &promptCaptureTransport{}
	http.DefaultTransport = capture
	t.Cleanup(func() { http.DefaultTransport = old })
	service, _ := serviceFixture(t, "")
	service.llm = llm.NewClient("explicit-mock")
	for _, kind := range []string{"concept", "source"} {
		for _, space := range []string{" ", "\u00a0", "\u2003"} {
			slug := "台北" + space + "café"
			for _, id := range []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "123456abcdef"} {
				_, reader := serviceFixture(t, `{"slug":"`+slug+`","title":"Display","body":"Concept body"}`+"\n")
				data, _ := json.Marshal(map[string]any{kind: map[string]string{id: slug}})
				if _, err := reader.WriteBytes(context.Background(), data, "cache/id_map.json"); err != nil {
					t.Fatal(err)
				}
				if kind == "source" {
					if _, err := reader.WriteBytes(context.Background(), []byte("Source body"), "wiki/sources/"+slug+".md"); err != nil {
						t.Fatal(err)
					}
				}
				result, err := service.SynthesizeWithError(context.Background(), reader, Request{Query: "test", Mode: "wiki"}, Result{Results: []search.Result{{Slug: slug, Title: "Display", Type: kind}}})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "source" && !strings.Contains(capture.user, "Source body") {
					t.Fatal("source hydrated from concept cache")
				}
				if len(result.Citations) != 1 || result.Citations[0].ID != id || result.Citations[0].Slug != slug || result.Citations[0].Type != kind {
					t.Fatalf("wrong snapshot/type identity: %+v", result.Citations)
				}
			}
		}
	}
}

func TestCitationMissingMapFailsBeforeLLM(t *testing.T) {
	service, reader := serviceFixture(t, `{"slug":"coffee","title":"Coffee","body":"Evidence"}`+"\n")
	// Override only ID-map access; persisted concepts still come from the same reader.
	missing := missingCitationMapReader{Store: reader}
	old := http.DefaultTransport
	calls := 0
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected LLM call") })
	t.Cleanup(func() { http.DefaultTransport = old })
	service.llm = llm.NewClient("explicit-mock")
	_, err := service.SynthesizeWithError(context.Background(), missing, Request{Query: "coffee", Mode: "wiki"}, Result{Results: []search.Result{{ID: "abcdef123456", Slug: "coffee", Title: "Coffee", Type: "concept"}}})
	if err == nil || !strings.Contains(err.Error(), "citation identity: read ID map") || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestCitationIDMapGenerationMismatchFailsBeforeLLM(t *testing.T) {
	service, reader := serviceFixture(t, `{"slug":"coffee","title":"Coffee","body":"Evidence"}`+"\n")
	old := http.DefaultTransport
	calls := 0
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected LLM call")
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	service.llm = llm.NewClient("explicit-mock")
	readerWithMismatch := generationMismatchCitationMapReader{Store: reader}
	_, err := service.SynthesizeWithError(context.Background(), readerWithMismatch, Request{Query: "coffee", Mode: "wiki"}, Result{Results: []search.Result{{ID: "000000000001", Slug: "coffee", Title: "Coffee", Type: "concept"}}})
	if !errors.Is(err, storage.ErrGenerationMismatch) || calls != 0 {
		t.Fatalf("err=%v calls=%d, want generation mismatch before LLM", err, calls)
	}
}

type missingCitationMapReader struct{ storage.Store }

func (r missingCitationMapReader) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if path == wikiindex.IDMapPath {
		return nil, os.ErrNotExist
	}
	return r.Store.ReadFile(ctx, path)
}

type generationMismatchCitationMapReader struct{ storage.Store }

func (r generationMismatchCitationMapReader) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if path == wikiindex.IDMapPath {
		return nil, storage.ErrGenerationMismatch
	}
	return r.Store.ReadFile(ctx, path)
}

func TestCitationIdentityResolverMultipleResults(t *testing.T) {
	ids := wikiindex.IDMap{
		Concept: map[string]string{"abcdef123456": "shared", "111111111111": "duplicate", "222222222222": "duplicate", "333333333333": "collision"},
		Source:  map[string]string{"123456abcdef": "shared", "333333333333": "other"},
	}
	resolver, err := newCitationIdentityResolver(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, slug, id, failure string }{
		{"concept", "shared", "abcdef123456", ""},
		{"source", "shared", "123456abcdef", ""},
		{"concept", "duplicate", "", "ambiguous mapping"},
		{"concept", "collision", "", "conflicting type"},
		{"source", "other", "", "conflicting type"},
		{"concept", "Shared", "", "missing mapping"},
		{"unknown", "shared", "", "invalid type"},
	} {
		got, err := resolver.resolve(search.Result{ID: "untrusted", Type: tc.kind, Slug: tc.slug})
		if tc.failure != "" {
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("%+v: result=%+v err=%v", tc, got, err)
			}
		} else if err != nil || got.ID != tc.id {
			t.Fatalf("%+v: result=%+v err=%v", tc, got, err)
		}
	}
	ids.Source[" invalid "] = "unselected"
	if _, err := newCitationIdentityResolver(ids); err == nil {
		t.Fatal("unselected unsafe map row accepted")
	}
}
