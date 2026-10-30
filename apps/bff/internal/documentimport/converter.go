package documentimport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrConvertedOutputTooLarge = errors.New("converted output exceeds the allowed size")

type Converter interface {
	Convert(context.Context, string, []byte) ([]byte, error)
}

type MarkItDownConverter struct {
	PythonPath string
	ScriptPath string
	Timeout    time.Duration
}

func (c MarkItDownConverter) Convert(ctx context.Context, filename string, input []byte) ([]byte, error) {
	if len(input) == 0 || len(input) > MaxOriginalBytes {
		return nil, ErrFileTooLarge
	}
	python := strings.TrimSpace(c.PythonPath)
	if python == "" {
		python = strings.TrimSpace(os.Getenv("DOCUMENT_IMPORT_PYTHON"))
	}
	if python == "" {
		python = "python3"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	convertCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	file, err := os.CreateTemp("", "lwc-document-import-*."+filepath.Ext(filename)[1:])
	if err != nil {
		return nil, &ConversionFailure{Class: "resource_limit", MessageKey: "resource_limit", Retryable: true, Cause: err}
	}
	inputPath := file.Name()
	defer os.Remove(inputPath)
	if _, err := file.Write(input); err != nil {
		_ = file.Close()
		return nil, &ConversionFailure{Class: "resource_limit", MessageKey: "resource_limit", Retryable: true, Cause: err}
	}
	if err := file.Close(); err != nil {
		return nil, &ConversionFailure{Class: "resource_limit", MessageKey: "resource_limit", Retryable: true, Cause: err}
	}

	script := c.ScriptPath
	if strings.TrimSpace(script) == "" {
		script = converterScriptPath()
	}
	cmd := exec.CommandContext(convertCtx, python, script, inputPath)
	cmd.Env = converterEnvironment()
	var output limitedBuffer
	output.limit = MaxMarkdownBytes
	var diagnostic limitedBuffer
	diagnostic.limit = 16 << 10
	cmd.Stdout = &output
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		if errors.Is(convertCtx.Err(), context.DeadlineExceeded) {
			return nil, &ConversionFailure{Class: "conversion_timeout", MessageKey: "conversion_timeout", Retryable: true, Cause: err}
		}
		if errors.Is(output.err, ErrConvertedOutputTooLarge) {
			return nil, &ConversionFailure{Class: "resource_limit", MessageKey: "resource_limit", Retryable: false, Cause: output.err}
		}
		class := classifyConverterFailure(diagnostic.String())
		return nil, &ConversionFailure{Class: class, MessageKey: class, Retryable: class == "converter_unavailable", Cause: err}
	}
	if output.err != nil {
		return nil, &ConversionFailure{Class: "resource_limit", MessageKey: "resource_limit", Retryable: false, Cause: output.err}
	}
	if len(bytes.TrimSpace(output.Bytes())) == 0 {
		return nil, &ConversionFailure{Class: "no_readable_text", MessageKey: "no_readable_text", Retryable: false, Cause: ErrNoReadableText}
	}
	return bytes.TrimSpace(output.Bytes()), nil
}

func converterScriptPath() string {
	for _, candidate := range []string{"/app/convert.py", "../../apps/document-import-worker/convert.py", "apps/document-import-worker/convert.py"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "convert.py"
}

type ConversionFailure struct {
	Class      string
	MessageKey string
	Retryable  bool
	Cause      error
}

func (e *ConversionFailure) Error() string { return e.Class }

func (e *ConversionFailure) Unwrap() error { return e.Cause }

type limitedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if len(p) > b.limit-b.Len() {
		b.err = ErrConvertedOutputTooLarge
		return 0, b.err
	}
	return b.Buffer.Write(p)
}

func converterEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func classifyConverterFailure(diagnostic string) string {
	message := strings.ToLower(diagnostic)
	switch {
	case strings.Contains(message, "no module named 'markitdown'") || strings.Contains(message, "no module named \"markitdown\""):
		return "converter_unavailable"
	case strings.Contains(message, "password") || strings.Contains(message, "encrypt"):
		return "password_protected"
	case strings.Contains(message, "not a zip") || strings.Contains(message, "badzip") || strings.Contains(message, "invalid pdf") || strings.Contains(message, "unexpected eof"):
		return "corrupt_file"
	default:
		return "conversion_error"
	}
}

func conversionFailure(err error) Failure {
	var failure *ConversionFailure
	if errors.As(err, &failure) {
		return Failure{Stage: FailureStageConvert, Class: failure.Class, SafeMessageKey: failure.MessageKey, DiagnosticID: diagnosticID(), Retryable: failure.Retryable}
	}
	return Failure{Stage: FailureStageConvert, Class: "conversion_error", SafeMessageKey: "conversion_error", DiagnosticID: diagnosticID()}
}

func diagnosticID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value[:])
}

func LockFileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
