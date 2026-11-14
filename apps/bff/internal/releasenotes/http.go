package releasenotes

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

type HTTPHandler struct{ reader Reader }

func NewHTTPHandler(source ReadStore, runtime BuildInfo) *HTTPHandler {
	return &HTTPHandler{reader: Reader{Store: source, Runtime: runtime}}
}

func (h *HTTPHandler) Register(router gin.IRoutes) {
	router.GET("/api/v1/public/release-notes", h.Index)
	router.GET("/api/v1/public/release-notes/:product_version", h.Content)
}

func (h *HTTPHandler) Index(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	response, err := h.reader.Index(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "release_notes_unavailable"})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *HTTPHandler) Content(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	locale := c.Query("locale")
	if locale != "en" && locale != "zh-TW" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_release_notes_locale"})
		return
	}
	response, err := h.reader.Content(c.Request.Context(), c.Param("product_version"), locale)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, response)
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "release_notes_not_found"})
	case errors.Is(err, ErrLocaleUnavailable):
		c.JSON(http.StatusNotFound, gin.H{"error": "release_notes_locale_unavailable"})
	case errors.Is(err, ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_release_notes_request"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "release_notes_unavailable"})
	}
}
