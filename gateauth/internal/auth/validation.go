package auth

import "strings"

// ValidPathSegment reports whether value can be safely used as one path segment.
func ValidPathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`+"\x00")
}
