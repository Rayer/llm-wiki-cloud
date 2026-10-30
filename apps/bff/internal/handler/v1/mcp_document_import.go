package v1

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayer/llm-wiki-bff/internal/documentimport"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxDocumentImportMCPRequestBytes = 8 << 20
	mcpImportDocumentDescription     = "Import actual PDF, DOCX, PPTX, XLSX, or XLS file bytes into this Project. Supply the complete file as Base64 with a stable client_submission_id. The tool stores and accepts the original, then an isolated offline MarkItDown worker reports conversion and source-save status. It does not run OCR or start a second pipeline dispatcher."
	mcpGetDocumentImportDescription  = "Read the durable status of one document import in this authorized Project by document_id or client_submission_id. The result distinguishes conversion, source save, and pipeline status."
)

type mcpImportDocumentInput struct {
	Filename           string `json:"filename"`
	ClientSubmissionID string `json:"client_submission_id"`
	BatchID            string `json:"batch_id,omitempty"`
	ContentBase64      string `json:"content_base64"`
}

type mcpGetDocumentImportInput struct {
	DocumentID         string `json:"document_id,omitempty"`
	ClientSubmissionID string `json:"client_submission_id,omitempty"`
}

var mcpImportDocumentInputSchema = json.RawMessage(fmt.Sprintf(`{
	"type":"object",
	"properties":{
		"filename":{"type":"string","minLength":1,"maxLength":512},
		"client_submission_id":{"type":"string","minLength":1,"maxLength":128},
		"batch_id":{"type":"string","maxLength":128},
		"content_base64":{"type":"string","minLength":1,"maxLength":%d,"description":"Standard Base64 encoding of the complete original file, up to 5 MiB decoded."}
	},
	"required":["filename","client_submission_id","content_base64"],
	"additionalProperties":false
}`, base64.StdEncoding.EncodedLen(documentimport.MaxOriginalBytes)))

var mcpGetDocumentImportInputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"document_id":{"type":"string","maxLength":128},
		"client_submission_id":{"type":"string","maxLength":128}
	},
	"additionalProperties":false
}`)

func (h *Handler) callMCPImportDocument(ctx context.Context, _ *mcp.CallToolRequest, input mcpImportDocumentInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" || !hasMCPCapability(identity.capabilities, mcpQueryCapability) || !hasMCPCapability(identity.capabilities, mcpRawWriteCapability) {
		return mcpDocumentImportError("authorization_required", "This Project grant does not allow document import."), nil, nil
	}
	if identity.revalidate != nil {
		if err := identity.revalidate(ctx); err != nil {
			return mcpDocumentImportError("authorization_changed", "The Project grant is no longer available."), nil, nil
		}
	}
	if h == nil || h.firestore == nil || h.firestore.Raw() == nil || h.documentImports == nil || h.documentImports.Repository == nil || h.documentImports.Starter == nil || h.store == nil {
		return mcpDocumentImportError("unavailable", "Document import is unavailable."), nil, nil
	}
	if h.isDemoUser(identity.userID) {
		return mcpDocumentImportError("forbidden", "Demo projects cannot import files."), nil, nil
	}
	projectSnap, err := scopedfirestore.Collection(h.firestore.Raw(), "projects").Doc(projectDocID(identity.userID, identity.projectID)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return mcpDocumentImportError("not_found", "The authorized Project is unavailable."), nil, nil
	}
	if err != nil || projectSnap.Data()["status"] != rawUploadReadyStatus {
		return mcpDocumentImportError("unavailable", "Document import is unavailable until the Project is ready."), nil, nil
	}
	if len(input.ContentBase64) > base64.StdEncoding.EncodedLen(documentimport.MaxOriginalBytes) {
		return mcpDocumentImportError("file_too_large", "The original file exceeds 5 MiB."), nil, nil
	}
	data, err := base64.StdEncoding.Strict().DecodeString(input.ContentBase64)
	if err != nil {
		return mcpDocumentImportError("invalid_base64", "content_base64 must encode the complete original file."), nil, nil
	}
	if len(data) > documentimport.MaxOriginalBytes {
		return mcpDocumentImportError("file_too_large", "The original file exceeds 5 MiB."), nil, nil
	}
	objects := h.store.Scope(identity.userID, identity.projectID)
	conditional, ok := objects.(documentimport.ObjectStore)
	if !ok {
		return mcpDocumentImportError("unavailable", "Document storage is unavailable."), nil, nil
	}
	if input.BatchID != "" && documentimport.ValidateSubmissionID(input.BatchID) != nil {
		return mcpDocumentImportError("invalid_request", "batch_id is invalid."), nil, nil
	}
	record, err := h.documentImports.Accept(ctx, conditional, identity.userID, identity.projectID, input.ClientSubmissionID, input.BatchID, input.Filename, data)
	if err != nil {
		return mcpDocumentImportError(mcpDocumentImportErrorCode(err), mcpDocumentImportSafeMessage(err)), nil, nil
	}
	statusURL := "/api/v1/projects/" + url.PathEscape(identity.projectID) + "/document-imports/" + url.PathEscape(record.DocumentID)
	siteURL := "/sources?project_id=" + url.QueryEscape(identity.projectID) + "&conversion_status=pending&document_id=" + url.QueryEscape(record.DocumentID)
	return mcpDocumentImportResult(map[string]any{
		"status": "accepted", "message": "The file is accepted. Conversion runs asynchronously. After the source is saved, automatic processing follows this Project's settings. Open the Project page to check status or resolve a failure.",
		"document": record, "status_url": statusURL, "site_url": siteURL,
	}, false), nil, nil
}

func (h *Handler) callMCPGetDocumentImport(ctx context.Context, _ *mcp.CallToolRequest, input mcpGetDocumentImportInput) (*mcp.CallToolResult, any, error) {
	identity, ok := ctx.Value(mcpProjectKeyIdentityContextKey{}).(mcpProjectKeyIdentity)
	if !ok || identity.userID == "" || identity.projectID == "" || !hasMCPCapability(identity.capabilities, mcpQueryCapability) {
		return mcpDocumentImportError("authorization_required", "This Project grant does not allow document status reads."), nil, nil
	}
	if identity.revalidate != nil {
		if err := identity.revalidate(ctx); err != nil {
			return mcpDocumentImportError("authorization_changed", "The Project grant is no longer available."), nil, nil
		}
	}
	if h == nil || h.documentImports == nil || h.documentImports.Repository == nil {
		return mcpDocumentImportError("unavailable", "Document status is unavailable."), nil, nil
	}
	if (input.DocumentID == "") == (input.ClientSubmissionID == "") {
		return mcpDocumentImportError("invalid_request", "Provide exactly one of document_id or client_submission_id."), nil, nil
	}
	var record documentimport.Record
	var err error
	if input.DocumentID != "" {
		record, err = h.documentImports.Repository.Get(ctx, identity.userID, identity.projectID, input.DocumentID)
	} else {
		var found bool
		record, found, err = h.documentImports.Repository.BySubmission(ctx, identity.userID, identity.projectID, input.ClientSubmissionID)
		if err == nil && !found {
			err = documentimport.ErrNotFound
		}
	}
	if err != nil {
		return mcpDocumentImportError(mcpDocumentImportErrorCode(err), mcpDocumentImportSafeMessage(err)), nil, nil
	}
	if record.DeletionState == documentimport.DeletionComplete {
		return mcpDocumentImportError("not_found", "Document import is no longer available."), nil, nil
	}
	return mcpDocumentImportResult(map[string]any{"status": "ok", "document": documentimport.PublicRecord(record)}, false), nil, nil
}

func mcpDocumentImportResult(payload any, isError bool) *mcp.CallToolResult {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mcpDocumentImportError("unavailable", "Document import is unavailable.")
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: json.RawMessage(encoded),
		IsError:           isError,
	}
}

func mcpDocumentImportError(code, message string) *mcp.CallToolResult {
	return mcpDocumentImportResult(map[string]any{"error": message, "code": code}, true)
}

func mcpDocumentImportErrorCode(err error) string {
	switch {
	case errors.Is(err, documentimport.ErrInvalidFilename), errors.Is(err, documentimport.ErrUnsupportedFile), errors.Is(err, documentimport.ErrInvalidContent):
		return "unsupported_file"
	case errors.Is(err, documentimport.ErrInvalidSubmissionID):
		return "invalid_request"
	case errors.Is(err, documentimport.ErrEmptyFile):
		return "empty_file"
	case errors.Is(err, documentimport.ErrFileTooLarge):
		return "file_too_large"
	case errors.Is(err, documentimport.ErrConflict):
		return "submission_conflict"
	case errors.Is(err, documentimport.ErrNotFound):
		return "not_found"
	case errors.Is(err, documentimport.ErrAcceptanceUnknown):
		return "acceptance_unknown"
	default:
		return "unavailable"
	}
}

func mcpDocumentImportSafeMessage(err error) string {
	switch mcpDocumentImportErrorCode(err) {
	case "unsupported_file":
		return "The file type or file content is not supported."
	case "empty_file":
		return "The original file is empty."
	case "file_too_large":
		return "The original file exceeds 5 MiB."
	case "submission_conflict":
		return "client_submission_id already identifies a different file."
	case "not_found":
		return "Document import was not found in this Project."
	case "acceptance_unknown":
		return "The acceptance outcome is unknown; query the same client_submission_id before retrying."
	default:
		return "Document import is temporarily unavailable."
	}
}
