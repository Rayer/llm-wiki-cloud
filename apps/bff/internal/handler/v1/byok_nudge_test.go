package v1

import (
	"strings"
	"testing"

	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

func TestBYOKActionableObservationUsesSupportedCauseAndProjectTarget(t *testing.T) {
	tests := []struct {
		reason   string
		contains string
	}{
		{reason: "authentication", contains: "rejected authentication"},
		{reason: "quota", contains: "account quota"},
		{reason: "billing", contains: "billing restriction"},
		{reason: "permission", contains: "rejected the permissions"},
		{reason: "model_configuration", contains: "configured model or request settings"},
	}
	for _, test := range tests {
		t.Run(test.reason, func(t *testing.T) {
			observation := byokActionableObservation("wiki-a", "query", "", test.reason)
			if observation.Title != "Custom LLM Provider needs attention" || observation.ProjectID != "wiki-a" ||
				observation.ProjectName != "" || observation.Severity != nudge.SeverityAttention ||
				!strings.Contains(observation.Body, test.contains) || strings.Contains(observation.Body, "API key is invalid") {
				t.Fatalf("unexpected BYOK observation: %+v", observation)
			}
			if len(observation.Actions) != 1 || observation.Actions[0].Target == nil ||
				observation.Actions[0].Target.Kind != "project-ai-service" || observation.Actions[0].Target.ProjectID != "wiki-a" ||
				observation.Actions[0].Label != "Open LLM Provider settings" {
				t.Fatalf("unexpected BYOK action: %+v", observation.Actions)
			}
			route, err := nudge.ResolveTarget(*observation.Actions[0].Target)
			if err != nil || route != "/profile?project_id=wiki-a#project-ai-service" {
				t.Fatalf("BYOK action route=%q err=%v", route, err)
			}
		})
	}
}
