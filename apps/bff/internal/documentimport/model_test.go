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
		name string
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
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	file, err := w.Create("[Content_Types].xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`<Types><Override ContentType="` + contentType + `"/></Types>`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
