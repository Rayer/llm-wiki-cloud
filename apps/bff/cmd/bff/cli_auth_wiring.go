package main

import (
	"cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	handlerv1 "github.com/rayer/llm-wiki-bff/internal/handler/v1"
)

// wireCLIAuthorities connects CLI account middleware to the same Firestore
// owner records and durable refresh-session authority used by Auth.
func wireCLIAuthorities(h *handlerv1.Handler, fs *firestore.Client, sessions *auth.RefreshSessionAuthority) {
	if h == nil {
		return
	}
	if sessions != nil {
		h.SetCLISessionVerifier(sessions.VerifyCLIAccessSession)
	}
	if fs != nil {
		h.SetCLIProjectAuthorizer(auth.FirestoreProjectOwnerAuthorizer(fs))
	}
}
