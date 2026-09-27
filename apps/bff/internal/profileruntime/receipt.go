// Package profileruntime shares durable runtime evidence between workers and dispatchers.
package profileruntime

import (
	"github.com/rayer/llm-wiki-bff/internal/profileartifacts"
	"time"
)

const CompileReceiptsCollection = "compile_receipts"

// CompileReceipt is written only after a successful compile publication. The manifest digest
// binds the actual publisher-returned manifest; its object generation is the
// confirmed current.json generation, including successful ambiguous-write readback.
// Dispatchers must recheck the pinned Profile revision/digest before deriving.
type CompileReceipt struct {
	UserID                    string                                 `firestore:"user_id"`
	ProjectID                 string                                 `firestore:"project_id"`
	ExecutionID               string                                 `firestore:"execution_id"`
	ProfileRevision           int64                                  `firestore:"profile_revision"`
	RequirementsDigest        string                                 `firestore:"requirements_digest"`
	ContentGeneration         string                                 `firestore:"content_generation"`
	ManifestSHA256            string                                 `firestore:"manifest_sha256"`
	CanonicalConceptsDigest   string                                 `firestore:"canonical_concepts_digest"`
	ManifestGeneration        int64                                  `firestore:"manifest_generation"`
	ConsumedBootstrapGuidance *profileartifacts.BootstrapGuidanceRef `firestore:"consumed_bootstrap_guidance"`
	CreatedAt                 time.Time                              `firestore:"created_at"`
}
