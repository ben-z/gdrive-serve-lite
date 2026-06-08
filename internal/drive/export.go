package drive

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	GoogleAppsMIMEPrefix    = "application/vnd.google-apps."
	ShortcutMIME            = "application/vnd.google-apps.shortcut"
	DocumentMIME            = "application/vnd.google-apps.document"
	SpreadsheetMIME         = "application/vnd.google-apps.spreadsheet"
	PresentationMIME        = "application/vnd.google-apps.presentation"
	DrawingMIME             = "application/vnd.google-apps.drawing"
	AppsScriptMIME          = "application/vnd.google-apps.script"
	GoogleVidsMIME          = "application/vnd.google-apps.vid"
	OpenXMLDocumentMIME     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	OpenXMLSpreadsheetMIME  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	OpenXMLPresentationMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

var ErrUnsupportedExport = errors.New("unsupported export")

type ExportFormat struct {
	Format    string `json:"format"`
	MIMEType  string `json:"mimeType"`
	Extension string `json:"extension,omitempty"`
}

var exportFormatsByName = map[string]ExportFormat{
	"docx": {Format: "docx", MIMEType: OpenXMLDocumentMIME, Extension: "docx"},
	"odt":  {Format: "odt", MIMEType: "application/vnd.oasis.opendocument.text", Extension: "odt"},
	"rtf":  {Format: "rtf", MIMEType: "application/rtf", Extension: "rtf"},
	"pdf":  {Format: "pdf", MIMEType: "application/pdf", Extension: "pdf"},
	"txt":  {Format: "txt", MIMEType: "text/plain", Extension: "txt"},
	"html": {Format: "html", MIMEType: "application/zip", Extension: "zip"},
	"epub": {Format: "epub", MIMEType: "application/epub+zip", Extension: "epub"},
	"md":   {Format: "md", MIMEType: "text/markdown", Extension: "md"},

	"xlsx": {Format: "xlsx", MIMEType: OpenXMLSpreadsheetMIME, Extension: "xlsx"},
	"ods":  {Format: "ods", MIMEType: "application/x-vnd.oasis.opendocument.spreadsheet", Extension: "ods"},
	"csv":  {Format: "csv", MIMEType: "text/csv", Extension: "csv"},
	"tsv":  {Format: "tsv", MIMEType: "text/tab-separated-values", Extension: "tsv"},

	"pptx": {Format: "pptx", MIMEType: OpenXMLPresentationMIME, Extension: "pptx"},
	"odp":  {Format: "odp", MIMEType: "application/vnd.oasis.opendocument.presentation", Extension: "odp"},
	"jpg":  {Format: "jpg", MIMEType: "image/jpeg", Extension: "jpg"},
	"png":  {Format: "png", MIMEType: "image/png", Extension: "png"},
	"svg":  {Format: "svg", MIMEType: "image/svg+xml", Extension: "svg"},

	"json": {Format: "json", MIMEType: "application/vnd.google-apps.script+json", Extension: "json"},
	"mp4":  {Format: "mp4", MIMEType: "video/mp4", Extension: "mp4"},
}

var defaultSourceExport = map[string]string{
	DocumentMIME:     "docx",
	SpreadsheetMIME:  "xlsx",
	PresentationMIME: "pptx",
	DrawingMIME:      "svg",
	AppsScriptMIME:   "json",
	GoogleVidsMIME:   "mp4",
}

var sourceExportFormats = map[string][]string{
	DocumentMIME:     {"docx", "pdf", "odt", "rtf", "txt", "html", "epub", "md"},
	SpreadsheetMIME:  {"xlsx", "pdf", "ods", "csv", "tsv", "html"},
	PresentationMIME: {"pptx", "pdf", "odp", "txt", "jpg", "png", "svg"},
	DrawingMIME:      {"svg", "pdf", "jpg", "png"},
	AppsScriptMIME:   {"json"},
	GoogleVidsMIME:   {"mp4"},
}

var exportPriority = map[string]int{
	OpenXMLDocumentMIME:     0,
	OpenXMLSpreadsheetMIME:  1,
	OpenXMLPresentationMIME: 2,
	"image/svg+xml":         3,
	"application/pdf":       4,
	"text/plain":            5,
}

func ExportFormatFor(s string) (ExportFormat, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return ExportFormat{}, errors.New("export format or MIME type is required")
	}
	key := strings.TrimPrefix(strings.ToLower(raw), ".")
	if format, ok := exportFormatsByName[key]; ok {
		return format, nil
	}
	if strings.Contains(raw, "/") {
		if format, ok := exportFormatForMIME(raw); ok {
			return format, nil
		}
		return ExportFormat{Format: raw, MIMEType: raw}, nil
	}
	return ExportFormat{}, fmt.Errorf("%w: unknown export format %q", ErrUnsupportedExport, raw)
}

func DefaultExportFormat(file File) (ExportFormat, bool) {
	if !IsWorkspaceFile(file) {
		return ExportFormat{}, false
	}
	if key, ok := defaultSourceExport[file.MIMEType]; ok {
		format := exportFormatsByName[key]
		if len(file.ExportLinks) == 0 || file.ExportLinks[format.MIMEType] != "" {
			return format, true
		}
	}
	formats := SupportedExportFormats(file)
	if len(formats) == 0 {
		return ExportFormat{}, false
	}
	return formats[0], true
}

func SupportedExportFormats(file File) []ExportFormat {
	if !IsWorkspaceFile(file) {
		return nil
	}
	if len(file.ExportLinks) > 0 {
		formats := make([]ExportFormat, 0, len(file.ExportLinks))
		for mimeType := range file.ExportLinks {
			if format, ok := exportFormatForMIME(mimeType); ok {
				formats = append(formats, format)
				continue
			}
			formats = append(formats, ExportFormat{Format: mimeType, MIMEType: mimeType})
		}
		sortExportFormats(formats)
		return formats
	}
	keys, ok := sourceExportFormats[file.MIMEType]
	if !ok {
		return nil
	}
	formats := make([]ExportFormat, 0, len(keys))
	for _, key := range keys {
		formats = append(formats, exportFormatsByName[key])
	}
	return formats
}

func SupportsExportFormat(file File, format ExportFormat) bool {
	for _, supported := range SupportedExportFormats(file) {
		if strings.EqualFold(supported.MIMEType, format.MIMEType) {
			return true
		}
	}
	return false
}

func IsWorkspaceFile(file File) bool {
	return strings.HasPrefix(file.MIMEType, GoogleAppsMIMEPrefix) &&
		file.MIMEType != FolderMIME &&
		file.MIMEType != ShortcutMIME
}

func annotateFileExports(file *File) {
	formats := SupportedExportFormats(*file)
	if len(formats) == 0 {
		return
	}
	file.ExportFormats = formats
	if defaultFormat, ok := DefaultExportFormat(*file); ok {
		file.DefaultExport = &defaultFormat
	}
}

func annotateListExports(list *FileList) {
	for i := range list.Files {
		annotateFileExports(&list.Files[i])
	}
}

func exportFormatForMIME(mimeType string) (ExportFormat, bool) {
	for _, format := range exportFormatsByName {
		if strings.EqualFold(format.MIMEType, mimeType) {
			return format, true
		}
	}
	return ExportFormat{}, false
}

func sortExportFormats(formats []ExportFormat) {
	sort.Slice(formats, func(i, j int) bool {
		leftPriority, leftOK := exportPriority[formats[i].MIMEType]
		rightPriority, rightOK := exportPriority[formats[j].MIMEType]
		if leftOK != rightOK {
			return leftOK
		}
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if formats[i].Format != formats[j].Format {
			return formats[i].Format < formats[j].Format
		}
		return formats[i].MIMEType < formats[j].MIMEType
	})
}
