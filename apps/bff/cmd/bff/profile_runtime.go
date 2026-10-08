package main

import (
	"github.com/rayer/llm-wiki-bff/internal/config"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/profilederive"
	"github.com/rayer/llm-wiki-bff/internal/profiletags"
)

// Existing DeepSeek configuration supplies free-form derivation; the existing
// TypeSafe key names supply the typed Jev tag evaluator. No resources are created.
func productionProfileDispatcher(h *handlerv1.Handler, cfg config.Config) *handlerv1.ProfileDispatcher {
	evaluator := profiletags.NewJevEvaluator(cfg.TypeSafeAPIKey)
	client := llm.NewClientWithOptions(cfg.DeepSeekAPIKey, llm.ClientOptions{
		Model: cfg.LLMModel, BaseURL: cfg.LLMBaseURL, RequestTimeoutSeconds: cfg.LLMRequestTimeoutSeconds,
	})
	var provider *profilederive.Provider
	if client != nil {
		provider = profilederive.NewProvider(client)
	}
	return &handlerv1.ProfileDispatcher{Handler: h, Provider: provider, Evaluator: evaluator,
		Policy:   profiletags.ProviderPolicy{ConfiguredModel: "jev-latest", AcceptedReturnedModels: []string{"jev-1.13.0"}, PromptVersion: profiletags.JevPromptVersion, SchemaVersion: profiletags.DecisionSchema},
		Audience: cfg.ProfileRuntimeAudience, ServiceAccount: cfg.ProfileRuntimeServiceAccount,
	}
}
