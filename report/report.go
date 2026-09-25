// Package report provides template-based generation of assessment reports.
//
// Reports are rendered using Go's text/template package and a
// domain.ReportPayload. Templates have access to helper functions for common
// reporting operations, including sorting and counting findings, retrieving
// captured HTTP request-response pairs, truncating and sanitizing raw data,
// and embedding image artifacts as data URIs.
//
// A Generator manages the report templates stored in the application's
// configuration directory. WithConfigDir creates the templates directory and
// installs the embedded default template when one does not already exist.
// Templates may then be listed, loaded, saved, validated, and executed.
//
// Some template functions require access to persisted assessment data.
// Generator therefore uses a Repository to retrieve request-response rows,
// WebSocket transcripts, and artifact contents while rendering a report.
//
// Template execution uses missingkey=error, so references to missing map keys
// cause rendering to fail rather than silently producing empty output.
package report

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/domain"
)

//go:embed default_template.md
var defaultTemplate string

var _ domain.ReportGenerator = (*Generator)(nil)

// Repository defines the database methods used by report template functions.
type Repository interface {
	// GetRequestResponseRow retrieves a captured request and response pair by ID.
	GetRequestResponseRow(id uuid.UUID) (*domain.RequestResponseRow, error)
	// GetRequestResponseRows retrieves multiple complete request-response pairs, including any associated notes, for a slice of request IDs in a single query.
	GetRequestResponseRows(ids []uuid.UUID) ([]*domain.RequestResponseRow, error)
	// GetArtifact retrieves an artifact with its raw data by ID.
	GetArtifact(uuid.UUID) (*domain.Artifact, error)
	// GetConnectionByRequestID retrieves a WebSocket connection by its HTTP upgrade request ID.
	GetConnectionByRequestID(requestID uuid.UUID) (*domain.WebSocketConnection, error)
	// GetMessagesByRequestID retrieves WebSocket messages by their HTTP upgrade request ID.
	GetMessagesByRequestID(requestID uuid.UUID) ([]*domain.WebSocketMessage, error)
}

// WebSocketTranscript contains a persisted WebSocket connection and its messages.
type WebSocketTranscript struct {
	Connection *domain.WebSocketConnection
	Messages   []*domain.WebSocketMessage
}

// Option configures a report Generator.
type Option func(*Generator) error

// Generator manages report templates and renders report payloads.
type Generator struct {
	repo         Repository
	templatesDir string
}

// TemplateInfo describes a regular file in the templates directory.
type TemplateInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// NewGenerator creates a Generator using repo and applies each option in order.
//
// It returns the first error produced by an option.
func NewGenerator(repo Repository, options ...Option) (*Generator, error) {
	g := &Generator{
		repo: repo,
	}
	for _, option := range options {
		if err := option(g); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// WithConfigDir configures a Generator to store templates in the templates
// subdirectory of configDir.
//
// The directory is created when necessary. The embedded default template is
// installed as default_template.md unless that file already exists.
func WithConfigDir(configDir string) Option {
	return func(g *Generator) error {
		g.templatesDir = filepath.Join(configDir, "templates")

		if err := os.MkdirAll(g.templatesDir, 0700); err != nil {
			return fmt.Errorf("creating templates dir %s: %w", g.templatesDir, err)
		}

		defaultTemplateFile := filepath.Join(g.templatesDir, "default_template.md")

		if _, err := os.Stat(defaultTemplateFile); errors.Is(err, os.ErrNotExist) {
			err = os.WriteFile(defaultTemplateFile, []byte(defaultTemplate), 0600)
			if err != nil {
				return fmt.Errorf("writing default template file to path %s: %w", defaultTemplateFile, err)
			}
		} else if err != nil {
			return fmt.Errorf("checking if template file exists at %s: %w", defaultTemplateFile, err)
		}

		return nil
	}
}

// ListTemplates returns the filenames of available report templates.
//
// Directories and hidden files are omitted. When default_template.md exists,
// it is returned as the first entry.
func (g *Generator) ListTemplates() ([]string, error) {
	entries, err := os.ReadDir(g.templatesDir)
	if err != nil {
		return nil, fmt.Errorf("reading templates dir %s: %w", g.templatesDir, err)
	}

	const defaultTemplate = "default_template.md"
	var hasDefault bool

	templates := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		if entry.Name() == defaultTemplate {
			hasDefault = true
			continue
		}

		templates = append(templates, entry.Name())
	}

	if hasDefault {
		templates = append([]string{defaultTemplate}, templates...)
	}

	return templates, nil
}

// ListTemplateDetails returns regular report templates with their sizes, sorted by name.
func (g *Generator) ListTemplateDetails() ([]TemplateInfo, error) {
	entries, err := os.ReadDir(g.templatesDir)
	if err != nil {
		return nil, fmt.Errorf("reading templates dir %s: %w", g.templatesDir, err)
	}
	items := make([]TemplateInfo, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("reading template info %s: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() {
			items = append(items, TemplateInfo{Name: entry.Name(), Size: info.Size()})
		}
	}
	return items, nil
}

// LoadTemplate reads a template from the configured templates directory.
//
// name must be a local filename. Absolute paths, parent-directory references,
// and subdirectories are rejected.
func (g *Generator) LoadTemplate(name string) ([]byte, error) {
	templateName := strings.TrimSpace(name)

	if templateName == "" {
		return nil, errors.New("invalid template name: cannot be empty")
	}

	if !filepath.IsLocal(templateName) {
		return nil, errors.New("invalid template name: absolute paths and parent directory references are not allowed")
	}

	if filepath.Base(templateName) != templateName {
		return nil, errors.New("invalid template name: subdirectories not allowed")
	}

	templatePath := filepath.Join(g.templatesDir, templateName)

	data, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", templateName, err)
	}
	return data, nil
}

// SaveTemplate writes data to a template in the configured templates
// directory.
//
// name must be a local filename. Absolute paths, parent-directory references,
// and subdirectories are rejected. An existing template with the same name is
// overwritten.
func (g *Generator) SaveTemplate(name string, data []byte) error {
	templateName := strings.TrimSpace(name)

	if templateName == "" {
		return errors.New("invalid template name: cannot be empty")
	}

	if !filepath.IsLocal(templateName) {
		return errors.New("invalid template name: absolute paths and parent directory references are not allowed")
	}

	if filepath.Base(templateName) != templateName {
		return errors.New("invalid template name: subdirectories not allowed")
	}

	templatePath := filepath.Join(g.templatesDir, templateName)
	if err := os.WriteFile(templatePath, data, 0600); err != nil {
		return fmt.Errorf("writing template %s: %w", templateName, err)
	}
	return nil
}

// RestoreDefaultTemplate overwrites default_template.md with the embedded template.
func RestoreDefaultTemplate(generator domain.ReportGenerator) error {
	return generator.SaveTemplate("default_template.md", []byte(defaultTemplate))
}

// FuncMap returns the functions made available to report templates.
//
// Some functions retrieve request-response rows, WebSocket transcripts, and
// artifact data through the Generator's Repository. Template authors should
// therefore expect those functions to return execution errors when repository
// operations fail.
func (g *Generator) FuncMap() template.FuncMap {
	return template.FuncMap{
		"severityCount": func(findings []*domain.Finding) map[string]int {
			severityCounts := map[string]int{
				"Critical":      0,
				"High":          0,
				"Medium":        0,
				"Low":           0,
				"Informational": 0,
			}
			for _, finding := range findings {
				if finding == nil {
					continue
				}
				if _, ok := severityCounts[finding.Severity]; ok {
					severityCounts[finding.Severity]++
				}
			}
			return severityCounts

		},
		"inc": func(i int) int {
			return i + 1
		},
		"upper": strings.ToUpper,
		"join":  strings.Join,
		"truncate": func(input []byte, maxLen int) []byte {
			if maxLen <= 0 || len(input) <= maxLen {
				return input
			}
			suffix := []byte("\n\n[... TRUNCATED ...]")
			result := make([]byte, maxLen+len(suffix))
			copy(result, input[:maxLen])
			copy(result[maxLen:], suffix)
			return result
		},
		"sortFindings": func(findings []*domain.Finding) []*domain.Finding {
			severityOrder := map[string]int{
				"Critical":      1,
				"High":          2,
				"Medium":        3,
				"Low":           4,
				"Informational": 5,
			}
			slices.SortFunc(findings, func(a, b *domain.Finding) int {
				if a.Severity != b.Severity {
					return cmp.Compare(
						severityOrder[a.Severity],
						severityOrder[b.Severity],
					)
				}
				return cmp.Compare(b.CVSSScore, a.CVSSScore)
			})
			return findings
		},
		"getRows": func(ids []uuid.UUID) ([]*domain.RequestResponseRow, error) {
			rows, err := g.repo.GetRequestResponseRows(ids)
			if err != nil {
				return nil, err
			}
			return rows, nil
		},
		"getRow": func(id uuid.UUID) (*domain.RequestResponseRow, error) {
			row, err := g.repo.GetRequestResponseRow(id)
			if err != nil {
				return nil, fmt.Errorf("getting row %s: %w", id, err)
			}
			return row, nil
		},
		"getWebSocket": func(requestID uuid.UUID) (*WebSocketTranscript, error) {
			connection, err := g.repo.GetConnectionByRequestID(requestID)
			if err != nil {
				return nil, fmt.Errorf("getting websocket connection for request %s: %w", requestID, err)
			}

			messages, err := g.repo.GetMessagesByRequestID(requestID)
			if err != nil {
				return nil, fmt.Errorf("getting websocket messages for request %s: %w", requestID, err)
			}

			return &WebSocketTranscript{
				Connection: connection,
				Messages:   messages,
			}, nil
		},
		"artifactDataURI": func(metadata *domain.ArtifactMetadata) (string, error) {
			if !strings.HasPrefix(metadata.MimeType, "image/") {
				return "", fmt.Errorf("artifact %s is not an image: %s", metadata.ID, metadata.MimeType)
			}

			artifact, err := g.repo.GetArtifact(metadata.ID)
			if err != nil {
				return "", err
			}

			encoded := base64.StdEncoding.EncodeToString(artifact.Data)

			return fmt.Sprintf(
				"data:%s;base64,%s",
				metadata.MimeType,
				encoded,
			), nil
		},
		"isImage": func(metadata *domain.ArtifactMetadata) bool {
			return strings.HasPrefix(metadata.MimeType, "image/")
		},
		"cleanPrint": func(input []byte) string {
			var sb strings.Builder

			for _, b := range input {
				if (b >= 32 && b <= 126) || b == '\n' || b == '\r' || b == '\t' {
					sb.WriteByte(b)
				} else {
					fmt.Fprintf(&sb, "\\x%02X", b)
				}
			}
			return sb.String()

		},
		"toJSON": func(value any) (string, error) {
			data, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	}
}

// Execute parses rawTemplate and renders it using payload.
//
// Template map lookups use missingkey=error, causing execution to fail when a
// referenced map key does not exist. Repository-backed template function
// failures are also returned as execution errors.
func (g *Generator) Execute(rawTemplate []byte, payload *domain.ReportPayload) ([]byte, error) {
	var out bytes.Buffer

	tmpl, err := template.New("marasi_report").
		Option("missingkey=error").
		Funcs(g.FuncMap()).
		Parse(string(rawTemplate))
	if err != nil {
		return nil, err
	}

	err = tmpl.Execute(&out, payload)
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// Validate checks whether rawTemplate can be parsed and executed using an
// empty validation payload.
//
// Validation detects syntax errors, missing payload fields, missing map keys,
// and other errors encountered during execution. Repository-backed template
// functions may still fail if the template invokes them with values not
// present in the validation payload.
func (g *Generator) Validate(rawTemplate []byte) error {
	payload := &domain.ReportPayload{
		Metadata: domain.ReportMetadata{
			Title:            "Validation Report",
			Client:           "Client",
			Type:             "Assessment",
			IsDraft:          true,
			Scope:            "Scope",
			Assessor:         "Assessor",
			TruncateLength:   0,
			Start:            time.Now(),
			End:              time.Now(),
			CreatedAt:        time.Now(),
			CustomProperties: map[string]any{},
		},
		Findings:  []*domain.Finding{},
		TestCases: []*domain.TestCase{},
	}
	_, err := g.Execute(rawTemplate, payload)
	if err != nil {
		return fmt.Errorf("validating report template: %w", err)
	}
	return nil

}
