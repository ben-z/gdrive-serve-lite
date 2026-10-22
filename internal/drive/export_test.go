package drive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestGetFileAnnotatesExportFormats(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
				"id":"doc-1",
				"name":"Notes",
				"mimeType":"application/vnd.google-apps.document",
				"exportLinks":{
					"application/pdf":"https://example.invalid/pdf",
					"application/vnd.openxmlformats-officedocument.wordprocessingml.document":"https://example.invalid/docx"
				}
			}`), nil
		}),
	})

	file, err := client.GetFile(context.Background(), "doc-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if file.DefaultExport == nil {
		t.Fatal("DefaultExport is nil")
	}
	if file.DefaultExport.Format != "docx" {
		t.Fatalf("DefaultExport = %+v, want docx", file.DefaultExport)
	}
	if len(file.ExportFormats) != 2 {
		t.Fatalf("ExportFormats = %d, want 2", len(file.ExportFormats))
	}
}

func TestExportFormatFor(t *testing.T) {
	format, err := ExportFormatFor("pdf")
	if err != nil {
		t.Fatal(err)
	}
	if format.MIMEType != "application/pdf" || format.Extension != "pdf" {
		t.Fatalf("format = %+v", format)
	}

	format, err = ExportFormatFor(openXMLSpreadsheetMIME)
	if err != nil {
		t.Fatal(err)
	}
	if format.Format != "xlsx" {
		t.Fatalf("format = %+v, want xlsx", format)
	}

	_, err = ExportFormatFor("definitely-not-a-format")
	if !errors.Is(err, ErrUnsupportedExport) {
		t.Fatalf("error = %v, want ErrUnsupportedExport", err)
	}
}

func TestSupportsExportFormatUsesExportLinksWhenPresent(t *testing.T) {
	pdf, err := ExportFormatFor("pdf")
	if err != nil {
		t.Fatal(err)
	}
	docx, err := ExportFormatFor("docx")
	if err != nil {
		t.Fatal(err)
	}
	file := File{
		MIMEType: documentMIME,
		ExportLinks: map[string]string{
			"application/pdf": "https://example.invalid/pdf",
		},
	}
	if !SupportsExportFormat(file, pdf) {
		t.Fatal("expected pdf to be supported")
	}
	if SupportsExportFormat(file, docx) {
		t.Fatal("did not expect docx to be supported")
	}
}

func TestSupportsExportFormatFallsBackToSourceType(t *testing.T) {
	pdf, err := ExportFormatFor("pdf")
	if err != nil {
		t.Fatal(err)
	}
	xlsx, err := ExportFormatFor("xlsx")
	if err != nil {
		t.Fatal(err)
	}
	file := File{MIMEType: documentMIME}
	if !SupportsExportFormat(file, pdf) {
		t.Fatal("expected pdf to be supported")
	}
	if SupportsExportFormat(file, xlsx) {
		t.Fatal("did not expect xlsx to be supported")
	}
}

func TestExportUsesFilesExportEndpoint(t *testing.T) {
	client := NewClient(testConfig(), &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %q", req.Method)
			}
			if req.URL.Path != "/drive/v3/files/doc-1/export" {
				t.Fatalf("path = %q", req.URL.Path)
			}
			if req.URL.Query().Get("mimeType") != "application/pdf" {
				t.Fatalf("mimeType = %q", req.URL.Query().Get("mimeType"))
			}
			if req.URL.Query().Get("supportsAllDrives") != "" {
				t.Fatalf("unexpected supportsAllDrives = %q", req.URL.Query().Get("supportsAllDrives"))
			}
			return textResponse(http.StatusOK, "%PDF"), nil
		}),
	})

	resp, err := client.Export(context.Background(), "doc-1", "application/pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "%PDF" {
		t.Fatalf("body = %q", body)
	}
}

func TestGoogleVidsHasNoImplicitExportFormat(t *testing.T) {
	file := File{MIMEType: "application/vnd.google-apps.vid"}
	if !IsWorkspaceFile(file) {
		t.Fatal("Google Vids must retain Workspace classification")
	}
	if formats := SupportedExportFormats(file); len(formats) != 0 {
		t.Fatalf("unsupported formats advertised: %+v", formats)
	}
	if format, ok := DefaultExportFormat(file); ok {
		t.Fatalf("unsupported default advertised: %+v", format)
	}
	mp4, err := ExportFormatFor("mp4")
	if err != nil {
		t.Fatal(err)
	}
	if SupportsExportFormat(file, mp4) {
		t.Fatal("Vids incorrectly accepts explicit MP4 export")
	}
}

func TestExportDefaultsPreserveOtherTypesAndCustomLinks(t *testing.T) {
	for _, tt := range []struct {
		name, mimeType, format string
		exportLinks            map[string]string
	}{
		{"Docs", documentMIME, "docx", nil},
		{"Apps Script", appsScriptMIME, "json", nil},
		{"custom MP4", "application/vnd.google-apps.custom", "mp4", map[string]string{"video/mp4": "https://example.invalid/custom"}},
		{"blob MP4", "video/mp4", "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			file := File{MIMEType: tt.mimeType, ExportLinks: tt.exportLinks}
			format, ok := DefaultExportFormat(file)
			if tt.format == "" {
				if ok || IsWorkspaceFile(file) || len(SupportedExportFormats(file)) != 0 {
					t.Fatal("ordinary MP4 became a Workspace export")
				}
				return
			}
			alias, err := ExportFormatFor(tt.format)
			if err != nil || !ok || format != alias || !SupportsExportFormat(file, alias) {
				t.Fatalf("default = %+v/%t, alias = %+v, error = %v", format, ok, alias, err)
			}
		})
	}
}

func TestODSAliasMatchesDocumentedMetadata(t *testing.T) {
	const mimeType = "application/vnd.oasis.opendocument.spreadsheet"
	want := ExportFormat{Format: "ods", MIMEType: mimeType, Extension: "ods"}
	format, err := ExportFormatFor("ods")
	if err != nil || format != want {
		t.Errorf("ODS alias = %+v, error = %v", format, err)
	}
	file := File{MIMEType: spreadsheetMIME, ExportLinks: map[string]string{mimeType: "https://example.invalid/ods"}}
	annotateFileExports(&file)
	if len(file.ExportFormats) != 1 || file.ExportFormats[0] != want || file.DefaultExport == nil || *file.DefaultExport != want {
		t.Errorf("ODS metadata annotations: formats = %+v, default = %+v", file.ExportFormats, file.DefaultExport)
	}
	if !SupportsExportFormat(file, format) {
		t.Error("ODS alias does not match documented export metadata")
	}
}
