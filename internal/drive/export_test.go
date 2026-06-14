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
