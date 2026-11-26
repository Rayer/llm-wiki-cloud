package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/nudge"
)

func (h *Handler) recordBYOKActionableFailure(ctx context.Context, userID, projectID, profileID string,
	profileRevision, bindingRevision, pipelineSequence int64, reason, operation, executionID string, startedAt time.Time) error {
	if h.firestore == nil || h.firestore.Raw() == nil {
		return fmt.Errorf("BYOK reminder storage unavailable")
	}
	projectSnapshot, err := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(userID + "_" + projectID).Get(ctx)
	if err != nil {
		return err
	}
	projectData := projectSnapshot.Data()
	if projectData["user_id"] != userID || projectData["project_id"] != projectID {
		return fmt.Errorf("BYOK reminder Project scope unavailable")
	}
	projectName, _ := projectData["name"].(string)
	if strings.TrimSpace(projectName) == "" {
		projectName = projectID
	}
	witness := nudge.ProducerWitness{
		Kind: "byok-failure", ProjectID: projectID, ProfileID: profileID,
		ProfileRevision: profileRevision, BindingRevision: bindingRevision,
		FailureReason: reason, Trigger: operation, ExecutionID: executionID, ProjectPipelineSequence: pipelineSequence,
		AttemptStartedAt: startedAt.UTC().Truncate(time.Microsecond),
	}
	observation := byokActionableObservation(projectID, operation, executionID, reason)
	observation.ProjectName = projectName
	body, err := json.Marshal(struct {
		Witness     nudge.ProducerWitness `json:"witness"`
		Observation nudge.Observation     `json:"observation"`
	}{witness, observation})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	input := nudge.ObservationInput{
		Category: nudge.CategoryBYOKActionable, SourceKey: "byok:" + projectID,
		ProducerWitness: witness, PayloadDigest: hex.EncodeToString(digest[:]),
		AttentionMaterialRevision: fmt.Sprintf("profile-revision:%d;binding-revision:%d;reason:%s", profileRevision, bindingRevision, reason),
		Observation:               observation,
	}
	repository := nudge.NewRepository(h.firestore.Raw())
	input.ExpectedSourceRevision, err = repository.ReadSourceRevision(ctx, userID, input.Category, input.SourceKey)
	if err != nil {
		return err
	}
	_, err = repository.ApplyBYOKObservation(ctx, userID, input)
	return err
}

func clearBYOKNudgesForBoundProjects(ctx context.Context, client *firestore.Client, owner string, boundProjects []string) {
	repository := nudge.NewRepository(client)
	prefix := owner + "/"
	seen := make(map[string]struct{}, len(boundProjects))
	for _, boundProject := range boundProjects {
		projectID, ok := strings.CutPrefix(boundProject, prefix)
		if !ok || projectID == "" {
			continue
		}
		if _, exists := seen[projectID]; exists {
			continue
		}
		seen[projectID] = struct{}{}
		if _, err := repository.ClearBYOKForSettings(ctx, owner, projectID); err != nil {
			log.Printf("BYOK nudge clear unavailable")
		}
	}
}

func byokActionableObservation(projectID, operation, executionID, reason string) nudge.Observation {
	body := "The custom LLM Provider used by this Project was rejected during a " + operation + " operation. Review LLM Provider settings."
	switch reason {
	case "authentication":
		body = "The provider rejected authentication for the custom LLM Provider used by this Project. Review its credential in LLM Provider settings."
	case "quota":
		body = "The provider reported that the account quota for the custom LLM Provider used by this Project is exhausted. Review the provider account and LLM Provider settings."
	case "billing":
		body = "The provider reported a billing restriction for the custom LLM Provider used by this Project. Review the provider account and LLM Provider settings."
	case "permission":
		body = "The provider rejected the permissions for the custom LLM Provider used by this Project. Review its access and LLM Provider settings."
	case "model_configuration":
		body = "The provider rejected the configured model or request settings for the custom LLM Provider used by this Project. Review LLM Provider settings."
	}
	if operation == "pipeline" && executionID != "" {
		body += " The Pipeline run " + executionID + " did not publish."
	}
	target := &nudge.Target{Kind: "project-ai-service", ProjectID: projectID}
	return nudge.Observation{
		ProjectID: projectID, Title: "Custom LLM Provider needs attention", Body: body,
		Severity: nudge.SeverityAttention,
		Actions:  []nudge.Action{{ID: "review-ai-settings", Kind: "link", Label: "Open LLM Provider settings", Target: target}},
	}
}
