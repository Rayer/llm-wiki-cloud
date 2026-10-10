package storage

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	MaxRawSyncFileBytes  int64 = 10 << 20
	MaxRawSyncTotalBytes int64 = 512 << 20
	MaxRawSyncFiles            = 10_000
	MaxRawSyncPathBytes        = 1024
)

var ErrRawSyncConflict = errors.New("raw file changed since it was listed")

// ErrRawSyncCommitUncertain marks a write error after the destination may have
// changed, so clients must verify occupancy before sending another write.
var ErrRawSyncCommitUncertain = errors.New("raw sync commit outcome is uncertain")

// RawSyncFile is one regular file beneath a project's raw/ prefix. Generation
// is opaque to clients and is pinned for conditional writes and reads.
type RawSyncFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Generation string `json:"generation"`
}

// RawSyncStore is the narrow streaming seam used only by the raw sync API.
type RawSyncStore interface {
	ListSyncRawFiles(context.Context) ([]RawSyncFile, error)
	OpenSyncRawFile(context.Context, string, string) (io.ReadCloser, int64, error)
	WriteSyncRawFile(context.Context, string, io.Reader, string, string) (string, error)
}

func ValidateRawSyncPath(value string) error {
	if value == "" || len(value) > MaxRawSyncPathBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\\\x00\r\n") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return errors.New("invalid raw-relative path")
	}
	if len(value) >= 2 && value[1] == ':' {
		return errors.New("invalid raw-relative path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("invalid raw-relative path")
		}
	}
	return nil
}

func ValidRawSyncSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
