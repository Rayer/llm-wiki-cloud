package main

import (
	"os"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/config"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
)

// Existing DeepSeek configuration supplies free-form derivation; the existing
// TypeSafe key names supply the typed Jev tag evaluator. No resources are created.
func productionProfileDispatcher(h *handlerv1.Handler, cfg config.Config) *handlerv1.ProfileDispatcher {
	key := os.Getenv("TYPESAFE_JEV_API_KEY")
	if key == "" {
		key = os.Getenv("TYPESAFE_API_KEY")
	}
	evaluator := profiletags.NewJevEvaluator(key)
	client := llm.NewClient(cfg.DeepSeekAPIKey)
	var provider *profilederive.Provider
	if client != nil {
		provider = profilederive.NewProvider(client)
	}
	return &handlerv1.ProfileDispatcher{Handler: h, Provider: provider, Evaluator: evaluator,
		Policy:   profiletags.ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: profiletags.JevPromptVersion, SchemaVersion: profiletags.DecisionSchema},
		Audience: strings.TrimSpace(os.Getenv("PROFILE_RUNTIME_AUDIENCE")), ServiceAccount: strings.TrimSpace(os.Getenv("PROFILE_RUNTIME_SERVICE_ACCOUNT")),
	}
}
