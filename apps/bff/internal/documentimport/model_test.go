package documentimport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateUploadAcceptsOnlyDeclaredFormatsAndChecksSignatures(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{name: "report.pdf", data: []byte("%PDF-1.7\nbody"), mime: "application/pdf"},
		{name: "report.xls", data: append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, []byte("workbook")...), mime: "application/vnd.ms-excel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mediaType, digest, err := ValidateUpload(tc.name, tc.data)
			if err != nil || mediaType != tc.mime || len(digest) != 64 {
				t.Fatalf("ValidateUpload() = (%q, %q, %v)", mediaType, digest, err)
			}
		})
	}
	for ext, contentType := range map[string]string{
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
		"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml",
		"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
	} {
		t.Run(ext, func(t *testing.T) {
			data := officeFixture(t, contentType)
			if _, _, err := ValidateUpload("report."+ext, data); err != nil {
				t.Fatalf("ValidateUpload() error = %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{name: "old-word.doc", data: []byte("not accepted"), want: ErrUnsupportedFile},
		{name: "bad.pdf", data: []byte("not pdf"), want: ErrInvalidContent},
		{name: "../report.pdf", data: []byte("%PDF-1.7"), want: ErrInvalidFilename},
		{name: "report.pdf", data: nil, want: ErrEmptyFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ValidateUpload(tc.name, tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateUpload() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateUploadScansEntireOfficeArchiveBeforeAccepting(t *testing.T) {
	large := bytes.Repeat([]byte("x"), (64<<20)+1)
	contentTypes := []byte(`<Types><Override ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	for _, tc := range []struct {
		name    string
		entries []zipEntry
	}{
		{name: "content types first", entries: []zipEntry{{"[Content_Types].xml", contentTypes}, {"large.bin", large}}},
		{name: "content types middle", entries: []zipEntry{{"small-before.bin", []byte("before")}, {"[Content_Types].xml", contentTypes}, {"large.bin", large}}},
		{name: "content types last", entries: []zipEntry{{"large.bin", large}, {"small-before.bin", []byte("before")}, {"[Content_Types].xml", contentTypes}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := officeArchiveFixture(t, tc.entries...)
			if len(data) > MaxOriginalBytes {
				t.Fatalf("fixture compressed size = %d, want <= %d", len(data), MaxOriginalBytes)
			}
			if _, _, err := ValidateUpload("report.docx", data); !errors.Is(err, ErrArchiveResourceLimit) {
				t.Fatalf("ValidateUpload() error = %v, want %v", err, ErrArchiveResourceLimit)
			}
		})
	}
}

func TestValidateUploadAcceptsContentTypesAtAnyArchivePosition(t *testing.T) {
	contentTypes := []byte(`<Types><Override ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	for _, tc := range []struct {
		name    string
		entries []zipEntry
	}{
		{name: "first", entries: []zipEntry{{"[Content_Types].xml", contentTypes}, {"document.xml", []byte("body")}}},
		{name: "middle", entries: []zipEntry{{"before.xml", []byte("before")}, {"[Content_Types].xml", contentTypes}, {"document.xml", []byte("body")}}},
		{name: "last", entries: []zipEntry{{"document.xml", []byte("body")}, {"before.xml", []byte("before")}, {"[Content_Types].xml", contentTypes}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ValidateUpload("report.docx", officeArchiveFixture(t, tc.entries...)); err != nil {
				t.Fatalf("ValidateUpload() error = %v", err)
			}
		})
	}
}

func TestPublicRecordHidesStorageAndDispatchDetails(t *testing.T) {
	record := PublicRecord(Record{
		DocumentID: "document-1", OriginalObjectPath: ".lwc/document-imports/document-1/original",
		OriginalGeneration: 4, AttemptID: "attempt-1", JobExecutionID: "job-execution-1",
		MarkdownObjectPath: ".lwc/document-imports/document-1/source.md", DispatchState: "started",
	})
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	encoded := string(data)
	for _, forbidden := range []string{".lwc/", "attempt-1", "job-execution-1", "started", "original_generation"} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("public record leaked %q: %s", forbidden, encoded)
		}
	}
	if !bytes.Contains(data, []byte(`"pipeline_ref":null`)) {
		t.Fatalf("pipeline_ref must remain explicitly null before source save: %s", encoded)
	}
}

func TestValidateSubmissionIDRequiresBoundedOpaqueValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "uuid", value: "9f5d24ca-0cef-4bcb-98c9-c06615be0c68", valid: true},
		{name: "opaque", value: "upload_01HT8NP6XTVP9M3", valid: true},
		{name: "empty", value: ""},
		{name: "path", value: "../document"},
		{name: "space", value: "id value"},
		{name: "control", value: "id\r\nX-Injected: yes"},
		{name: "oversized", value: strings.Repeat("x", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateSubmissionID(tc.value) == nil
			if got != tc.valid {
				t.Fatalf("ValidateSubmissionID(%q) valid=%t, want %t", tc.value, got, tc.valid)
			}
		})
	}
}

func officeFixture(t *testing.T, contentType string) []byte {
	t.Helper()
	return officeArchiveFixture(t, zipEntry{"[Content_Types].xml", []byte(`<Types><Override ContentType="` + contentType + `"/></Types>`)})
}

type zipEntry struct {
	name string
	data []byte
}

func officeArchiveFixture(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	for _, entry := range entries {
		file, err := w.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
