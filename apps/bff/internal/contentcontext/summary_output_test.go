package contentcontext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSummaryOutputAcceptsBoundedJSONText(t *testing.T) {
	got, err := ParseSummaryOutput([]byte(`{"summary":"Research papers and reading notes."}`))
	if err != nil || got != "Research papers and reading notes." {
		t.Fatalf("ParseSummaryOutput() = %q, %v", got, err)
	}
	text := strings.Repeat("😀", 1000)
	got, err = ParseSummaryOutput([]byte(`{"summary":"` + text + `"}`))
	if err != nil || utf8.RuneCountInString(got) != 1000 {
		t.Fatalf("1000-codepoint summary length=%d err=%v", utf8.RuneCountInString(got), err)
	}
}

func TestParseSummaryOutputRejectsInvalidOrUnboundedResponses(t *testing.T) {
	for _, raw := range []string{
		"",
		"null",
		`{"summary":""}`,
		`{"summary":"   "}`,
		`{"summary":null}`,
		`{"summary":4}`,
		`{"summary":"ok","other":"field"}`,
		`{"summary":"first","summary":"second"}`,
		`{"summary":"ok"} {}`,
		`{"summary":"` + strings.Repeat("😀", 1001) + `"}`,
	} {
		if _, err := ParseSummaryOutput([]byte(raw)); err == nil {
			t.Errorf("ParseSummaryOutput accepted %q", raw[:min(len(raw), 80)])
		}
	}
	if _, err := ParseSummaryOutput([]byte(strings.Repeat("x", SummaryMaxOutputBytes+1))); err == nil {
		t.Fatal("oversized provider response was accepted")
	}
}

func TestParseSummaryOverviewOutputValidatesWholeOverviewStrictly(t *testing.T) {
	raw := []byte(`{"summary":"Evidence from selected excerpts.","short_title":"Evidence notes","topic_tags":["research","reading"]}`)
	got, err := ParseSummaryOverviewOutput(raw)
	if err != nil || got.Summary != "Evidence from selected excerpts." || got.ShortTitle != "Evidence notes" || len(got.TopicTags) != 2 {
		t.Fatalf("ParseSummaryOverviewOutput() = %+v, %v", got, err)
	}
	for _, invalid := range []string{
		`{"summary":"ok","short_title":"title","topic_tags":["tag"],"extra":true}`,
		`{"summary":"ok","summary":"again","short_title":"title","topic_tags":["tag"]}`,
		`{"summary":"ok","short_title":"title","topic_tags":["tag"]} {}`,
		`{"summary":4,"short_title":"title","topic_tags":["tag"]}`,
		`{"summary":"ok","short_title":"","topic_tags":["tag"]}`,
		`{"summary":"ok","short_title":"` + strings.Repeat("x", 25) + `","topic_tags":["tag"]}`,
		`{"summary":"ok","short_title":"title","topic_tags":[]}`,
		`{"summary":"ok","short_title":"title","topic_tags":["tag","TAG"]}`,
		`{"summary":"ok","short_title":"title","topic_tags":["<b>tag</b>"]}`,
		`{"summary":"` + strings.Repeat("x", 1001) + `","short_title":"title","topic_tags":["tag"]}`,
	} {
		if _, err := ParseSummaryOverviewOutput([]byte(invalid)); err == nil {
			t.Errorf("ParseSummaryOverviewOutput accepted %q", invalid[:min(len(invalid), 90)])
		}
	}
	if _, err := ParseSummaryOverviewOutput([]byte(strings.Repeat("x", SummaryMaxOutputBytes+1))); err == nil {
		t.Fatal("oversized overview response was accepted")
	}
}
