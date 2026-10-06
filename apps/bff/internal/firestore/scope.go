package firestore

import (
	"os"
	"sync"

	cloudfirestore "cloud.google.com/go/firestore"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

var clientScopes sync.Map // map[*cloudfirestore.Client]localcloud.Scope

// RegisterLocalScope binds a Firestore client to one local worktree scope.
// An empty scope preserves the deployed top-level collection layout.
func RegisterLocalScope(client *cloudfirestore.Client, raw string) error {
	if client == nil {
		return nil
	}
	scope, err := localcloud.Parse(raw)
	if err != nil {
		return err
	}
	if scope == "" {
		clientScopes.Delete(client)
	} else {
		clientScopes.Store(client, scope)
	}
	return nil
}

// Collection resolves every top-level SDK consumer through the same scope
// root. Nested collections naturally remain under the resolved parent.
func Collection(client *cloudfirestore.Client, name string) *cloudfirestore.CollectionRef {
	if client == nil {
		return nil
	}
	if raw, ok := clientScopes.Load(client); ok {
		scope := raw.(localcloud.Scope)
		return client.Collection("local_scopes").Doc(string(scope)).Collection(name)
	}
	return client.Collection(name)
}

func forgetLocalScope(client *cloudfirestore.Client) { clientScopes.Delete(client) }

func configuredScope() string { return os.Getenv("LOCAL_CLOUD_SCOPE") }
