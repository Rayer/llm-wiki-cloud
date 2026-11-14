package profilelimits

import (
	"strings"
	"testing"
)

func TestRequirementLimitsUseUnicodeScalarsAndStableValidationOrder(t *testing.T) {
	if CountScalars("a\u0301\r\n👩‍🔬") != 7 {
		t.Fatalf("Unicode scalar count = %d, want 7", CountScalars("a\u0301\r\n👩‍🔬"))
	}
	rows := make([]string, MaxRequirementRows)
	for index := range rows {
		rows[index] = strings.Repeat("界", MaxProfileTextScalars/MaxRequirementRows)
	}
	if violation := ValidateRequirementTexts(rows); violation != nil {
		t.Fatalf("exact row/text/aggregate limits rejected: %+v", violation)
	}
	rows = []string{strings.Repeat("界", MaxRequirementTextScalars) + "🙂"}
	if violation := ValidateRequirementTexts(rows); violation == nil || violation.Kind != ViolationRequirementText || violation.Max != MaxRequirementTextScalars || violation.Actual != MaxRequirementTextScalars+1 {
		t.Fatalf("first overlong row violation = %+v", violation)
	}
	rows = make([]string, MaxRequirementRows+1)
	for index := range rows {
		rows[index] = "x"
	}
	if violation := ValidateRequirementTexts(rows); violation == nil || violation.Kind != ViolationRequirementCount || violation.Actual != MaxRequirementRows+1 {
		t.Fatalf("row limit should precede text limits, got %+v", violation)
	}
	rows = []string{strings.Repeat("🙂", 2_000), strings.Repeat("界", 2_000), strings.Repeat("a", 2_000), strings.Repeat("b", 2_000), strings.Repeat("c", 2_001)}
	if violation := ValidateRequirementTexts(rows); violation == nil || violation.Kind != ViolationRequirementText {
		t.Fatalf("single-row limit should precede aggregate limit, got %+v", violation)
	}
	rows[4] = strings.Repeat("c", 2_000)
	rows = append(rows, "z")
	if violation := ValidateRequirementTexts(rows); violation == nil || violation.Kind != ViolationProfileTextTotal || violation.Actual != 10_001 {
		t.Fatalf("aggregate overflow violation = %+v", violation)
	}
}

func TestGuidanceLimitUsesUnicodeScalars(t *testing.T) {
	if violation := ValidateGuidance(strings.Repeat("🙂", MaxGuidanceScalars)); violation != nil {
		t.Fatalf("exact guidance boundary rejected: %+v", violation)
	}
	if violation := ValidateGuidance(strings.Repeat("🙂", MaxGuidanceScalars+1)); violation == nil || violation.Kind != ViolationCompileGuidance || violation.Actual != MaxGuidanceScalars+1 {
		t.Fatalf("guidance boundary +1 violation = %+v", violation)
	}
}
