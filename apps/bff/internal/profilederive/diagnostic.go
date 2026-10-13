package profilederive

import (
	"errors"

	"github.com/rayer/llm-wiki-bff/internal/llm"
)

// FailureDiagnostic contains only fixed labels. It deliberately has no field
// for user requirements, provider text, response bodies, or error strings.
type FailureDiagnostic struct {
	Stage string `json:"stage" firestore:"stage"`
	Class string `json:"class" firestore:"class"`
}

const (
	FailureStageRevisionClaim      = "profile_revision_claim"
	FailureStageRequirements       = "profile_requirements_validate"
	FailureStageContentPin         = "profile_content_pin"
	FailureStageGenerationValidate = "profile_generation_validate"
	FailureStageProviderConfig     = "profile_provider_config"
	FailureStageBootstrapProvider  = "bootstrap_provider_call"
	FailureStageBootstrapModel     = "bootstrap_model_metadata"
	FailureStageBootstrapDecode    = "bootstrap_output_decode"
	FailureStageBootstrapValidate  = "bootstrap_output_validate"
	FailureStageBootstrapEncode    = "bootstrap_artifact_encode"
	FailureStageManualProvider     = "manual_provider_call"
	FailureStageManualModel        = "manual_model_metadata"
	FailureStageManualDecode       = "manual_output_decode"
	FailureStageManualValidate     = "manual_output_validate"
	FailureStageManualEncode       = "manual_artifact_encode"
	FailureStageArtifactStore      = "profile_artifact_store"
	FailureStageArtifactWrite      = "profile_artifact_write"
	FailureStageUnknown            = "profile_unknown"
)

const (
	FailureClassProviderTimeout          = "provider_timeout"
	FailureClassProviderCanceled         = "provider_canceled"
	FailureClassProviderAuthRejected     = "provider_auth_rejected"
	FailureClassProviderRateLimited      = "provider_rate_limited"
	FailureClassProviderServerError      = "provider_server_error"
	FailureClassProviderHTTPRejected     = "provider_http_rejected"
	FailureClassProviderResponseInvalid  = "provider_response_invalid"
	FailureClassProviderErrorUnknown     = "provider_error_unknown"
	FailureClassModelUnreported          = "model_unreported"
	FailureClassModelOutputDecodeFailed  = "model_output_decode_failed"
	FailureClassModelAccountingInvalid   = "model_accounting_invalid"
	FailureClassArtifactEncodeFailed     = "artifact_encode_failed"
	FailureClassInputInvalid             = "input_invalid"
	FailureClassContentUnavailable       = "content_unavailable"
	FailureClassGenerationInvalid        = "generation_invalid"
	FailureClassProviderUnavailable      = "provider_unavailable"
	FailureClassArtifactStoreUnavailable = "artifact_store_unavailable"
	FailureClassArtifactWriteFailed      = "artifact_write_failed"
	FailureClassStaleRevision            = "stale_revision"
	FailureClassUnknown                  = "unknown"
)

func (d FailureDiagnostic) Valid() bool {
	_, stageOK := allowedFailureStages[d.Stage]
	_, classOK := allowedFailureClasses[d.Class]
	return stageOK && classOK
}

var allowedFailureStages = map[string]struct{}{
	FailureStageRevisionClaim:      {},
	FailureStageRequirements:       {},
	FailureStageContentPin:         {},
	FailureStageGenerationValidate: {},
	FailureStageProviderConfig:     {},
	FailureStageBootstrapProvider:  {},
	FailureStageBootstrapModel:     {},
	FailureStageBootstrapDecode:    {},
	FailureStageBootstrapValidate:  {},
	FailureStageBootstrapEncode:    {},
	FailureStageManualProvider:     {},
	FailureStageManualModel:        {},
	FailureStageManualDecode:       {},
	FailureStageManualValidate:     {},
	FailureStageManualEncode:       {},
	FailureStageArtifactStore:      {},
	FailureStageArtifactWrite:      {},
	FailureStageUnknown:            {},
}

var allowedFailureClasses = map[string]struct{}{
	FailureClassProviderTimeout:          {},
	FailureClassProviderCanceled:         {},
	FailureClassProviderAuthRejected:     {},
	FailureClassProviderRateLimited:      {},
	FailureClassProviderServerError:      {},
	FailureClassProviderHTTPRejected:     {},
	FailureClassProviderResponseInvalid:  {},
	FailureClassProviderErrorUnknown:     {},
	FailureClassModelUnreported:          {},
	FailureClassModelOutputDecodeFailed:  {},
	FailureClassModelAccountingInvalid:   {},
	FailureClassArtifactEncodeFailed:     {},
	FailureClassInputInvalid:             {},
	FailureClassContentUnavailable:       {},
	FailureClassGenerationInvalid:        {},
	FailureClassProviderUnavailable:      {},
	FailureClassArtifactStoreUnavailable: {},
	FailureClassArtifactWriteFailed:      {},
	FailureClassStaleRevision:            {},
	FailureClassUnknown:                  {},
}

type failureDiagnosticError struct {
	diagnostic FailureDiagnostic
	cause      error
}

func (e *failureDiagnosticError) Error() string {
	if e.diagnostic.Class == FailureClassModelUnreported {
		return "provider_model_unreported"
	}
	switch e.diagnostic.Stage {
	case FailureStageBootstrapProvider, FailureStageManualProvider:
		return "provider call failed"
	default:
		return "Profile derivation failed"
	}
}
func (e *failureDiagnosticError) Unwrap() error { return e.cause }

func withFailureDiagnostic(stage, class string, cause error) error {
	diagnostic := FailureDiagnostic{Stage: stage, Class: class}
	if cause == nil || !diagnostic.Valid() {
		return cause
	}
	return &failureDiagnosticError{diagnostic: diagnostic, cause: cause}
}

// FailureDiagnosticFromError extracts only schema-approved labels from an
// error produced by the existing derivation path.
func FailureDiagnosticFromError(err error) (FailureDiagnostic, bool) {
	var diagnosticError *failureDiagnosticError
	if !errors.As(err, &diagnosticError) || !diagnosticError.diagnostic.Valid() {
		return FailureDiagnostic{}, false
	}
	return diagnosticError.diagnostic, true
}

func providerFailureClass(err error) string {
	switch llm.SafeErrorCategory(err) {
	case "timeout":
		return FailureClassProviderTimeout
	case "canceled":
		return FailureClassProviderCanceled
	case "provider_auth_rejected":
		return FailureClassProviderAuthRejected
	case "provider_rate_limited":
		return FailureClassProviderRateLimited
	case "provider_server_error":
		return FailureClassProviderServerError
	case "provider_http_rejected":
		return FailureClassProviderHTTPRejected
	case "provider_response_invalid_json":
		return FailureClassProviderResponseInvalid
	default:
		return FailureClassProviderErrorUnknown
	}
}
