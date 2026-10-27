//go:build !byokprofilefixture

package llm

import "net/http"

func profileFixtureTransport() http.RoundTripper { return nil }
