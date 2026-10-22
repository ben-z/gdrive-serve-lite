package drive

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	googleAppsMIMEPrefix    = "application/vnd.google-apps."
	shortcutMIME            = "application/vnd.google-apps.shortcut"
	documentMIME            = "application/vnd.google-apps.document"
	spreadsheetMIME         = "application/vnd.google-apps.spreadsheet"
	presentationMIME        = "application/vnd.google-apps.presentation"
	drawingMIME             = "application/vnd.google-apps.drawing"
	appsScriptMIME          = "application/vnd.google-apps.script"
	googleVidsMIME          = "application/vnd.google-apps.vid"
	openXMLDocumentMIME     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	openXMLSpreadsheetMIME  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	openXMLPresentationMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

var ErrUnsupportedExport = errors.New("unsupported export")

type ExportFormat struct {
	Format    string `json:"format"`
	MIMEType  string `json:"mimeType"`
	Extension string `json:"extension,omitempty"`
}

var exportFormatsByName = map[string]ExportFormat{
	"docx": {Format: "docx", MIMEType: openXMLDocumentMIME, Extension: "docx"},
	"odt":  {Format: "odt", MIMEType: "application/vnd.oasis.opendocument.text", Extension: "odt"},
	"rtf":  {Format: "rtf", MIMEType: "application/rtf", Extension: "rtf"},
	"pdf":  {Format: "pdf", MIMEType: "application/pdf", Extension: "pdf"},
	"txt":  {Format: "txt", MIMEType: "text/plain", Extension: "txt"},
	"html": {Format: "html", MIMEType: "application/zip", Extension: "zip"},
	"epub": {Format: "epub", MIMEType: "application/epub+zip", Extension: "epub"},
	"md":   {Format: "md", MIMEType: "text/markdown", Extension: "md"},

	"xlsx": {Format: "xlsx", MIMEType: openXMLSpreadsheetMIME, Extension: "xlsx"},
	"ods":  {Format: "ods", MIMEType: "application/x-vnd.oasis.opendocument.spreadsheet", Extension: "ods"},
	"csv":  {Format: "csv", MIMEType: "text/csv", Extension: "csv"},
	"tsv":  {Format: "tsv", MIMEType: "text/tab-separated-values", Extension: "tsv"},

	"pptx": {Format: "pptx", MIMEType: openXMLPresentationMIME, Extension: "pptx"},
	"odp":  {Format: "odp", MIMEType: "application/vnd.oasis.opendocument.presentation", Extension: "odp"},
	"jpg":  {Format: "jpg", MIMEType: "image/jpeg", Extension: "jpg"},
	"png":  {Format: "png", MIMEType: "image/png", Extension: "png"},
	"svg":  {Format: "svg", MIMEType: "image/svg+xml", Extension: "svg"},

	"json": {Format: "json", MIMEType: "application/vnd.google-apps.script+json", Extension: "json"},
	"mp4":  {Format: "mp4", MIMEType: "video/mp4", Extension: "mp4"},
}

var sourceExportFormats = map[string][]string{
	documentMIME:     {"docx", "pdf", "odt", "rtf", "txt", "html", "epub", "md"},
	spreadsheetMIME:  {"xlsx", "pdf", "ods", "csv", "tsv", "html"},
	presentationMIME: {"pptx", "pdf", "odp", "txt", "jpg", "png", "svg"},
	drawingMIME:      {"svg", "pdf", "jpg", "png"},
	appsScriptMIME:   {"json"},
	googleVidsMIME:   {"mp4"},
}

var exportPriority = map[string]int{
	openXMLDocumentMIME:     0,
	openXMLSpreadsheetMIME:  1,
	openXMLPresentationMIME: 2,
	"image/svg+xml":         3,
	"application/pdf":       4,
	"text/plain":            5,
}

func ExportFormatFor(s string) (ExportFormat, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return ExportFormat{}, fmt.Errorf("%w: export format or MIME type is required", ErrUnsupportedExport)
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
	if keys := sourceExportFormats[file.MIMEType]; len(keys) > 0 {
		format := exportFormatsByName[keys[0]]
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
	return strings.HasPrefix(file.MIMEType, googleAppsMIMEPrefix) &&
		file.MIMEType != FolderMIME &&
		file.MIMEType != shortcutMIME
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
