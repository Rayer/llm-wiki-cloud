package v1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/api/idtoken"
)

func TestScheduledPipelineRequiresTrustedOIDCIssuer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		issuer     string
		wantStatus int
	}{
		{name: "empty issuer", wantStatus: http.StatusUnauthorized},
		{name: "untrusted issuer", issuer: "https://attacker.example", wantStatus: http.StatusUnauthorized},
		{name: "Google issuer", issuer: "https://accounts.google.com", wantStatus: http.StatusServiceUnavailable},
		{name: "legacy Google issuer", issuer: "accounts.google.com", wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := New(nil, nil, nil, nil, nil, nil)
			h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "scheduler@example.iam.gserviceaccount.com")
			h.SetScheduledPipelineIdentityValidator(func(context.Context, string, string) (*idtoken.Payload, error) {
				return &idtoken.Payload{Issuer: tc.issuer, Audience: "https://bff.example", Claims: map[string]interface{}{
					"email": "scheduler@example.iam.gserviceaccount.com", "email_verified": true,
				}}, nil
			})
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/internal/pipeline/schedule", nil)
			c.Request.Header.Set("Authorization", "Bearer fixture-token")
			h.ScheduledPipeline(c)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s; want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
		})
	}
}

func TestScheduledPipelineStatusDoesNotInventAvailability(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	disabled := h.scheduledPipelineStatus(context.Background(), "owner", "project", now)
	if disabled.Availability != "disabled" || disabled.NextCheckAt != nil || disabled.LastResult != "" {
		t.Fatalf("unconfigured status=%+v; want disabled with no next check or inferred result", disabled)
	}
	h.SetScheduledPipelineConfig("0 * * * *", "UTC", "https://bff.example", "scheduler@example.iam.gserviceaccount.com")
	unavailable := h.scheduledPipelineStatus(context.Background(), "owner", "project", now)
	if unavailable.Availability == "available" || unavailable.NextCheckAt != nil || unavailable.LastResult != "" {
		t.Fatalf("uninspected status=%+v; want no availability promise or inferred result", unavailable)
	}
}

func TestNonDefinitivePipelineResponsesKeepInvocationUnknown(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{status: http.StatusBadRequest, want: true},
		{status: http.StatusUnauthorized, want: true},
		{status: http.StatusForbidden, want: true},
		{status: http.StatusServiceUnavailable},
		{status: http.StatusGatewayTimeout},
		{status: http.StatusConflict},
		{status: http.StatusTooManyRequests},
	} {
		if got := confirmedPipelineInvocationRejection(tc.status); got != tc.want {
			t.Errorf("HTTP %d confirmed rejection=%t; want %t", tc.status, got, tc.want)
		}
	}
}
