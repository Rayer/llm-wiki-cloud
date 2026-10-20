package urlintake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const MaxURLLength = 8192

var (
	ErrInvalidURL    = errors.New("invalid public URL")
	ErrUnresolvedURL = errors.New("public URL host could not be resolved")
)

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type defaultResolver struct{ resolver *net.Resolver }

func (r defaultResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return r.resolver.LookupNetIP(ctx, network, host)
}

// ValidatePublicURL rejects unsafe direct targets before a URL is sent to the
// Fetch provider. The provider does not expose redirect metadata, so this only
// validates the submitted target and its current DNS answer.
func ValidatePublicURL(ctx context.Context, raw string, resolver IPResolver) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > MaxURLLength || strings.TrimSpace(raw) != raw {
		return nil, ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || hasCredentialQuery(u.RawQuery) {
		return nil, ErrInvalidURL
	}
	if hasCredentialQuery(u.RawQuery) {
		return nil, ErrInvalidURL
	}

	host := u.Hostname()
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if !publicAddress(addr) {
			return nil, ErrInvalidURL
		}
		return u, nil
	}
	if resolver == nil {
		resolver = defaultResolver{resolver: net.DefaultResolver}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := resolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve public URL host: %w", ErrUnresolvedURL)
	}
	for _, addr := range addresses {
		if !publicAddress(addr) {
			return nil, ErrInvalidURL
		}
	}
	return u, nil
}

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	// Shared, documentation, benchmarking and reserved ranges are not public
	// destinations even though IsGlobalUnicast accepts some of them.
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("64:ff9b::/96"),
	} {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func hasCredentialQuery(raw string) bool {
	for _, part := range strings.Split(raw, "&") {
		key := strings.ToLower(strings.TrimSpace(strings.SplitN(part, "=", 2)[0]))
		decoded, err := url.QueryUnescape(key)
		if err == nil {
			key = decoded
		}
		switch key {
		case "token", "access_token", "api_key", "apikey", "key", "password", "passwd", "secret", "session", "sessionid", "authorization", "auth", "jwt":
			return true
		}
	}
	return false
}

// NormalizeURL applies only the identity rules accepted for intake: lowercase
// scheme/host, remove an explicit default port and utm_* parameters, and retain
// all other query components, path spelling and fragments verbatim.
func NormalizeURL(u *url.URL) (string, error) {
	if u == nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || hasCredentialQuery(u.RawQuery) {
		return "", ErrInvalidURL
	}
	normalized := *u
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	host := strings.ToLower(normalized.Hostname())
	port := normalized.Port()
	if (normalized.Scheme == "http" && port == "80") || (normalized.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		if port == "" {
			normalized.Host = "[" + host + "]"
		} else {
			normalized.Host = net.JoinHostPort(host, port)
		}
	} else if port == "" {
		normalized.Host = host
	} else {
		normalized.Host = net.JoinHostPort(host, port)
	}

	parts := strings.Split(normalized.RawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		if part == "" {
			continue
		}
		key := strings.SplitN(part, "=", 2)[0]
		decoded, err := url.QueryUnescape(key)
		if err == nil && strings.HasPrefix(strings.ToLower(decoded), "utm_") {
			continue
		}
		kept = append(kept, part)
	}
	normalized.RawQuery = strings.Join(kept, "&")
	if len(kept) == 0 {
		normalized.ForceQuery = false
	}
	return normalized.String(), nil
}
