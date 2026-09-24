package v1

import "github.com/rayer/llm-wiki-bff/internal/auth"

// SetCLISessionVerifier wires the shared durable session authority into the
// existing BFF account middleware. Production router startup owns the call.
func (h *Handler) SetCLISessionVerifier(verifier auth.CLIAccessSessionVerifier) {
	h.cliSessionVerifier = verifier
}

// SetCLIProjectAuthorizer wires the shared current project-owner check into
// the existing BFF account middleware. Production router startup owns the call.
func (h *Handler) SetCLIProjectAuthorizer(authorizer auth.ProjectOwnerAuthorizer) {
	h.cliProjectAuthorizer = authorizer
}
