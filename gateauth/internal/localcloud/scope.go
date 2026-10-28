package localcloud

import (
	"errors"
	"strings"
)

const firestoreRootCollection = "local_scopes"

// Scope identifies one worktree or explicitly disposable test run in the
// shared local-cloud project and bucket.
type Scope string

func Parse(raw string) (Scope, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 100 || raw == "." || raw == ".." {
		return "", errors.New("invalid local cloud scope")
	}
	for _, r := range raw {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", errors.New("invalid local cloud scope")
		}
	}
	return Scope(raw), nil
}

func (s Scope) FirestoreCollection(name string) string {
	if s == "" {
		return name
	}
	return firestoreRootCollection + "/" + string(s) + "/" + name
}

func (s Scope) ObjectPrefix() string {
	if s == "" {
		return ""
	}
	return firestoreRootCollection + "/" + string(s) + "/"
}
