package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const maxCLIResponseBytes = 1 << 20

var errCLIReauthenticationRequired = errors.New("CLI login is no longer valid; run `lwc-sync auth login`")

type cliAPIClient struct {
	origin      string
	httpClient  *http.Client
	credentials *cliLocalCredentials
}

type cliAPIStatusError struct{ status int }

func (e cliAPIStatusError) Error() string {
	return fmt.Sprintf("Auth service returned HTTP %d", e.status)
}

func newCLIAPIClient(origin string, credentials *cliLocalCredentials) (*cliAPIClient, error) {
	origin, err := normalizeAuthOrigin(origin)
	if err != nil {
		return nil, err
	}
	if credentials != nil {
		credentialOrigin, err := normalizeAuthOrigin(credentials.AuthHost)
		if err != nil || credentialOrigin != origin {
			return nil, errors.New("local credentials belong to a different auth/control-plane host")
		}
	}
	return &cliAPIClient{
		origin: origin,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		credentials: credentials,
	}, nil
}

func validateAuthOrigin(origin string) error {
	_, err := normalizeAuthOrigin(origin)
	return err
}

func normalizeAuthOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid auth/control-plane origin")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if scheme != "https" && scheme != "http" {
		return "", errors.New("auth/control-plane origin must use HTTPS")
	}
	if scheme == "http" {
		hostname := strings.ToLower(u.Hostname())
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("plain HTTP is allowed only for a loopback auth/control-plane origin")
		}
	}
	return scheme + "://" + host, nil
}

func (c *cliAPIClient) request(ctx context.Context, method, route string, input, output interface{}) error {
	if c == nil {
		return errors.New("Auth service client is unavailable")
	}
	if _, err := apiRouteURL(c.origin, route); err != nil {
		return errors.New("invalid Auth service API path")
	}
	response, err := c.do(ctx, method, route, input, c.credentials != nil && c.credentials.AccessToken != "")
	if err != nil {
		return err
	}
	if response.status == http.StatusUnauthorized && c.credentials != nil && c.credentials.RefreshToken != "" && !strings.HasSuffix(route, "/refresh") && !strings.HasSuffix(route, "/logout") {
		if err := c.refresh(ctx); err != nil {
			if errors.Is(err, errCLIReauthenticationRequired) {
				return err
			}
			return err
		}
		response, err = c.do(ctx, method, route, input, true)
		if err != nil {
			return err
		}
		if response.status == http.StatusUnauthorized {
			return errCLIReauthenticationRequired
		}
	}
	if response.status < 200 || response.status >= 300 {
		if response.status == http.StatusUnauthorized && c.credentials != nil && c.credentials.RefreshToken != "" {
			return errCLIReauthenticationRequired
		}
		return cliAPIStatusError{status: response.status}
	}
	if output != nil && len(response.body) > 0 {
		if err := json.Unmarshal(response.body, output); err != nil {
			return errors.New("Auth service returned an invalid response")
		}
	}
	return nil
}

func (c *cliAPIClient) refresh(ctx context.Context) error {
	if c.credentials == nil || strings.TrimSpace(c.credentials.RefreshToken) == "" {
		return errCLIReauthenticationRequired
	}
	response, err := c.do(ctx, http.MethodPost, "/api/v1/auth/cli/refresh", map[string]string{"refresh_token": c.credentials.RefreshToken}, false)
	if err != nil {
		return err
	}
	if response.status == http.StatusUnauthorized || response.status == http.StatusGone {
		return errCLIReauthenticationRequired
	}
	if response.status < 200 || response.status >= 300 {
		return cliAPIStatusError{status: response.status}
	}
	var rotated cliLocalCredentials
	if err := json.Unmarshal(response.body, &rotated); err != nil || rotated.AccessToken == "" || rotated.RefreshToken == "" || rotated.SessionID == "" {
		return errors.New("Auth service returned an invalid refresh response")
	}
	rotated.AuthHost = c.origin
	if rotated.UserID == "" {
		rotated.UserID = c.credentials.UserID
	}
	*c.credentials = rotated
	return nil
}

type cliAPIResponse struct {
	status int
	body   []byte
}

func (c *cliAPIClient) do(ctx context.Context, method, route string, input interface{}, authenticated bool) (cliAPIResponse, error) {
	requestURL, err := apiRouteURL(c.origin, route)
	if err != nil {
		return cliAPIResponse{}, errors.New("invalid Auth service API path")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return cliAPIResponse{}, errors.New("could not encode Auth service request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return cliAPIResponse{}, errors.New("could not create Auth service request")
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		if c.credentials == nil || strings.TrimSpace(c.credentials.AccessToken) == "" {
			return cliAPIResponse{}, errCLIReauthenticationRequired
		}
		request.Header.Set("Authorization", "Bearer "+c.credentials.AccessToken)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return cliAPIResponse{}, errors.New("could not reach the configured Auth/control-plane host")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCLIResponseBytes+1))
	if err != nil || len(data) > maxCLIResponseBytes {
		return cliAPIResponse{}, errors.New("Auth service response was unavailable or too large")
	}
	return cliAPIResponse{status: response.StatusCode, body: data}, nil
}

func apiRouteURL(origin, route string) (string, error) {
	if !strings.HasPrefix(route, "/api/v1/auth/") || strings.Contains(route, "\\") {
		return "", errors.New("invalid Auth API route")
	}
	u, err := url.ParseRequestURI(route)
	if err != nil || u.IsAbs() || u.Host != "" || path.Clean(u.Path) != u.Path || strings.HasPrefix(u.Path, "//") {
		return "", errors.New("invalid Auth API route")
	}
	return origin + route, nil
}
