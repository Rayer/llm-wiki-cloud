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
