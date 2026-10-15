package v1

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/observability"
	"github.com/rayer/llm-wiki-bff/internal/rawstatus"
)

type rawListResponse struct {
	Files []rawstatus.File `json:"files"`
}

func (h *Handler) RawList(c *gin.Context) {
	loadCtx, loadSpan := observability.StartDataPhase(c.Request.Context(), "load")
	c.Request = c.Request.WithContext(loadCtx)
	wikiStore, err := h.GetStore(c)
	if err != nil {
		observability.FinishDataSpan(loadSpan, observability.DataErrorOutcome(err))
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: generatedDataUnavailableMessage})
		return
	}
	observability.FinishDataSpan(loadSpan, "success")
	readCtx, readSpan := observability.StartDataPhase(c.Request.Context(), "read")
	files, err := wikiStore.ListRawFiles(readCtx)
	if err != nil {
		observability.FinishDataSpan(readSpan, observability.DataErrorOutcome(err))
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: generatedDataUnavailableMessage})
		return
	}
	observability.SetDataCount(readSpan, "items", int64(len(files)))
	observability.FinishDataSpan(readSpan, "success")

	artifact := rawstatus.EmptyArtifact(time.Now())
	readCtx, readSpan = observability.StartDataPhase(c.Request.Context(), "read")
	data, err := wikiStore.ReadFile(readCtx, rawstatus.Path)
	if err != nil {
		if !errors.Is(err, storage.ErrObjectNotExist) {
			observability.FinishDataSpan(readSpan, observability.DataErrorOutcome(err))
			observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: generatedDataUnavailableMessage})
			return
		}
		observability.FinishDataSpan(readSpan, "success")
	} else {
		observability.FinishDataSpan(readSpan, "success")
		artifact, err = rawstatus.Decode(data)
		if err != nil {
			observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: generatedDataUnavailableMessage})
			return
		}
	}

	observability.WriteDataJSON(c, http.StatusOK, rawListResponse{Files: rawstatus.Apply(files, artifact)})
}

func (h *Handler) RawPreview(c *gin.Context) {
	_, inputSpan := observability.StartDataPhase(c.Request.Context(), "input")
	filename := c.Param("filename")
	if err := validateRawUploadFilename(filename); err != nil {
		observability.FinishDataSpan(inputSpan, "rejected")
		observability.WriteDataJSON(c, http.StatusBadRequest, handler.ErrorResponse{Error: err.Error()})
		return
	}
	observability.FinishDataSpan(inputSpan, "success")

	loadCtx, loadSpan := observability.StartDataPhase(c.Request.Context(), "load")
	c.Request = c.Request.WithContext(loadCtx)
	wikiStore, err := h.GetStore(c)
	if err != nil {
		observability.FinishDataSpan(loadSpan, observability.DataErrorOutcome(err))
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw file unavailable"})
		return
	}
	observability.FinishDataSpan(loadSpan, "success")

	readCtx, readSpan := observability.StartDataPhase(c.Request.Context(), "read")
	data, err := wikiStore.ReadFile(readCtx, rawUploadRelativePath(filename))
	if err != nil {
		observability.FinishDataSpan(readSpan, observability.DataErrorOutcome(err))
		if errors.Is(err, storage.ErrObjectNotExist) {
			observability.WriteDataJSON(c, http.StatusNotFound, handler.ErrorResponse{Error: "raw file not found"})
			return
		}
		observability.WriteDataJSON(c, http.StatusInternalServerError, handler.ErrorResponse{Error: "raw file unavailable"})
		return
	}
	observability.SetDataCount(readSpan, "bytes", int64(len(data)))
	observability.FinishDataSpan(readSpan, "success")

	observability.WriteDataBytes(c, http.StatusOK, rawPreviewContentType(filename), data)
}

func rawPreviewContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}
