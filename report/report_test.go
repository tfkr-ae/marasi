package report

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/db"
	"github.com/tfkr-ae/marasi/domain"
)

func testRequest(t *testing.T, repo *db.Repository, metadata map[string]any) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	if metadata == nil {
		metadata = make(map[string]any)
	}

	req := &domain.ProxyRequest{
		ID:          id,
		Scheme:      "https",
		Method:      "GET",
		Host:        "marasi.app",
		Path:        "/",
		Raw:         []byte("GET / HTTP/1.1\r\nHost: marasi.app\r\n\r\n"),
		Metadata:    metadata,
		RequestedAt: time.Now(),
	}

	err = repo.InsertRequest(req)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	return id
}

func insertTestResponseAndGet(t *testing.T, repo *db.Repository, reqID uuid.UUID, metadata map[string]any) *domain.ProxyResponse {
	t.Helper()

	if metadata == nil {
		metadata = make(map[string]any)
	}

	rawResp := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 12\r\n\r\nHello Marasi")

	resp := &domain.ProxyResponse{
		ID:          reqID,
		Status:      "200 OK",
		StatusCode:  200,
		ContentType: "text/plain",
		Length:      "12",
		Raw:         rawResp,
		Metadata:    metadata,
		RespondedAt: time.Now().UTC().Truncate(time.Millisecond),
	}

	err := repo.InsertResponse(resp)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	return resp
}

func setupTestDB(t *testing.T) (*db.Repository, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp(t.TempDir(), "test_*.db")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	tempFile.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbConn, err := db.New(tempFile.Name(), logger)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	repo := db.NewProxyRepo(dbConn)

	teardown := func() {
		repo.Close()
		os.Remove(tempFile.Name())
	}

	return repo, teardown
}

func TestNewGenerator(t *testing.T) {
	t.Run("should create a new generator without error", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if generator == nil {
			t.Fatal("\nwanted:\ngenerator\ngot:\nnil")
		}
	})

	t.Run("should raise an error if option errors", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := errors.New("option failed")
		_, err := NewGenerator(repo, func(g *Generator) error {
			return want
		})

		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}

		if !errors.Is(err, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", err, want)
		}
	})
}

func TestWithConfigDir(t *testing.T) {
	t.Run("should create templates directory and default template file", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()

		generator, err := NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		wantTemplatesDir := filepath.Join(configDir, "templates")
		if generator.templatesDir != wantTemplatesDir {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", wantTemplatesDir, generator.templatesDir)
		}

		defaultTemplateFile := filepath.Join(wantTemplatesDir, "default_template.md")

		got, err := os.ReadFile(defaultTemplateFile)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != defaultTemplate {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", defaultTemplate, string(got))
		}
	})

	t.Run("should not overwrite existing default template file", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()
		templatesDir := filepath.Join(configDir, "templates")

		err := os.MkdirAll(templatesDir, 0700)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		defaultTemplateFile := filepath.Join(templatesDir, "default_template.md")
		want := "custom default template"

		err = os.WriteFile(defaultTemplateFile, []byte(want), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := os.ReadFile(defaultTemplateFile)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})
}

func TestGeneratorRestoreDefaultTemplate(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	if err := generator.SaveTemplate("default_template.md", []byte("custom template")); err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	if err := RestoreDefaultTemplate(generator); err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	got, err := generator.LoadTemplate("default_template.md")
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	if string(got) != defaultTemplate {
		t.Fatalf("\nwanted:\n%s\ngot:\n%s", defaultTemplate, got)
	}
}

func TestGeneratorListTemplates(t *testing.T) {
	t.Run("should list templates with default template first", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()

		generator, err := NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.WriteFile(filepath.Join(generator.templatesDir, "custom.md"), []byte("custom"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := generator.ListTemplates()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []string{"default_template.md", "custom.md"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should skip directories and hidden files", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()

		generator, err := NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.WriteFile(filepath.Join(generator.templatesDir, ".hidden.md"), []byte("hidden"), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = os.MkdirAll(filepath.Join(generator.templatesDir, "nested"), 0700)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := generator.ListTemplates()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []string{"default_template.md"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should raise an error if templates directory cannot be read", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		generator.templatesDir = filepath.Join(t.TempDir(), "missing")

		_, err = generator.ListTemplates()
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
}

func TestGeneratorListTemplateDetails(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	for name, content := range map[string]string{
		"a.md": "alpha", ".hidden.md": "hidden", "z.md": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}

	got, err := generator.ListTemplateDetails()
	if err != nil {
		t.Fatal(err)
	}
	want := []TemplateInfo{
		{Name: "a.md", Size: 5},
		{Name: "default_template.md", Size: int64(len(defaultTemplate))},
		{Name: "z.md", Size: 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wanted details %v, got %v", want, got)
	}

	if err := os.Remove(filepath.Join(dir, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "z.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "default_template.md")); err != nil {
		t.Fatal(err)
	}
	got, err = generator.ListTemplateDetails()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("wanted empty non-nil list, got %v, %v", got, err)
	}
}

func TestGeneratorAddTemplate(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "a.md")
	if err := os.WriteFile(source, []byte("{{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddTemplate(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(content) != "{{invalid" {
		t.Fatalf("bad template was not moved intact: %q, %v", content, err)
	}

	empty := filepath.Join(sourceDir, "empty.md")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddTemplate(empty); err != nil {
		t.Fatal(err)
	}
	items, err := generator.ListTemplateDetails()
	if err != nil || !reflect.DeepEqual(items, []TemplateInfo{
		{Name: "a.md", Size: 9},
		{Name: "default_template.md", Size: int64(len(defaultTemplate))},
		{Name: "empty.md", Size: 0},
	}) {
		t.Fatalf("wrong list after moves: %v, %v", items, err)
	}

	conflicting := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(conflicting, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddTemplate(conflicting); !errors.Is(err, ErrTemplateAlreadyExists) {
		t.Fatalf("wanted conflict, got %v", err)
	}
	if content, err := os.ReadFile(conflicting); err != nil || string(content) != "new" {
		t.Fatalf("conflicting source changed: %q, %v", content, err)
	}
	entries, err := os.ReadDir(filepath.Dir(conflicting))
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.md" {
		t.Fatalf("conflict left staging files: %v, %v", entries, err)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(content) != "{{invalid" {
		t.Fatalf("existing template changed: %q, %v", content, err)
	}
}

func TestGeneratorRemoveTemplate(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(configDir, "templates")
	if err := os.WriteFile(filepath.Join(dir, "dropped.md"), []byte("dropped"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, " custom.md "), []byte("spaced"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte("plain"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(configDir, "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := generator.RemoveTemplate("dropped.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dropped.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hand-dropped template still exists: %v", err)
	}
	if err := generator.RemoveTemplate("default_template.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "default_template.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default template still exists: %v", err)
	}
	if err := generator.RemoveTemplate("link.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink still exists: %v", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "target" {
		t.Fatalf("symlink target changed: %q, %v", content, err)
	}
	if err := generator.RemoveTemplate(" custom.md "); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "custom.md")); err != nil || string(content) != "plain" {
		t.Fatalf("removing a spaced name deleted the wrong file: %q, %v", content, err)
	}

	if err := generator.RemoveTemplate("missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wanted missing template, got %v", err)
	}
	for _, name := range []string{"", ".", "..", "nested/file.md", "/tmp/x.md", "a\x00b", "../secret.md"} {
		if err := generator.RemoveTemplate(name); !errors.Is(err, ErrInvalidTemplateName) {
			t.Errorf("name %q: wanted invalid name, got %v", name, err)
		}
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "secret" {
		t.Fatalf("invalid name deleted outside the templates directory: %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(dir, "custom.md")); err != nil || string(content) != "plain" {
		t.Fatalf("invalid name deleted a template: %q, %v", content, err)
	}
}

func TestGeneratorAddTemplatePreservesFilename(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), " custom.md ")
	if err := os.WriteFile(source, []byte("exact name"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generator.SaveTemplate("custom.md", []byte("other template")); err != nil {
		t.Fatal(err)
	}
	if err := generator.AddTemplate(source); err != nil {
		t.Fatal(err)
	}
	content, err := generator.LoadTemplate(" custom.md ")
	if err != nil || string(content) != "exact name" {
		t.Fatalf("cannot load added filename: %q, %v", content, err)
	}
	if err := generator.SaveTemplate(" custom.md ", []byte("edited")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{" custom.md ": "edited", "custom.md": "other template"} {
		content, err := generator.LoadTemplate(name)
		if err != nil || string(content) != want {
			t.Fatalf("saving %q changed the wrong file: %q, %v", name, content, err)
		}
	}
}

func TestGeneratorAddTemplatePublishedWhole(t *testing.T) {
	configDir := t.TempDir()
	writer, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "large.md")
	data := make([]byte, 16<<20)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- writer.AddTemplate(source) }()
	for {
		items, err := reader.ListTemplateDetails()
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Name == "large.md" && item.Size != int64(len(data)) {
				t.Fatalf("reader saw incomplete template: %d of %d bytes", item.Size, len(data))
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			content, err := reader.LoadTemplate("large.md")
			if err != nil || !reflect.DeepEqual(content, data) {
				t.Fatalf("published template is incomplete: %d bytes, %v", len(content), err)
			}
			return
		default:
			runtime.Gosched()
		}
	}
}

func TestGeneratorAddTemplateUnreadableFile(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "unreadable.md")
	if err := os.WriteFile(source, []byte("bytes"), 0000); err != nil {
		t.Fatal(err)
	}
	if file, err := os.Open(source); err == nil {
		file.Close()
		t.Skip("this user can read files without read permission")
	}
	if err := generator.AddTemplate(source); err != nil {
		t.Fatalf("regular file could not be moved: %v", err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	info, err := os.Lstat(filepath.Join(configDir, "templates", "unreadable.md"))
	if err != nil || !info.Mode().IsRegular() || info.Size() != 5 {
		t.Fatalf("destination not moved intact: %v, %v", info, err)
	}
}

func TestGeneratorAddTemplateRejectsInvalidSources(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := t.TempDir()
	file := filepath.Join(sourceDir, "file.md")
	if err := os.WriteFile(file, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sourceDir, "link.md")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(sourceDir, ".hidden.md")
	if err := os.WriteFile(hidden, nil, 0600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(configDir, "templates", "inside.md")
	if err := os.WriteFile(inside, []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(configDir, "templates"), alias); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"relative.md", link, sourceDir, hidden, inside, filepath.Join(alias, "inside.md"), file + "\x00bad",
	} {
		if err := generator.AddTemplate(source); !errors.Is(err, ErrInvalidTemplateSource) {
			t.Errorf("source %q: wanted invalid source, got %v", source, err)
		}
	}
	if err := generator.AddTemplate(filepath.Join(sourceDir, "missing.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wanted missing source, got %v", err)
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "content" {
		t.Fatalf("rejected source changed: %q, %v", content, err)
	}
}

func TestGeneratorAddTemplateConflictWithUnreadableSource(t *testing.T) {
	configDir := t.TempDir()
	generator, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "default_template.md")
	if err := os.WriteFile(source, []byte("source"), 0000); err != nil {
		t.Fatal(err)
	}
	if file, err := os.Open(source); err == nil {
		file.Close()
		t.Skip("this user can read files without read permission")
	}
	if err := generator.AddTemplate(source); !errors.Is(err, ErrTemplateAlreadyExists) {
		t.Fatalf("wanted conflict before opening source, got %v", err)
	}
	if _, err := os.Lstat(source); err != nil {
		t.Fatalf("conflicting source was removed: %v", err)
	}
}

func TestGeneratorAddTemplateAcrossGenerators(t *testing.T) {
	configDir := t.TempDir()
	first, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewGenerator(nil, WithConfigDir(configDir))
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{filepath.Join(t.TempDir(), "shared.md"), filepath.Join(t.TempDir(), "shared.md")}
	for i, source := range sources {
		if err := os.WriteFile(source, []byte{byte('a' + i)}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, generator := range []*Generator{first, second} {
		wg.Add(1)
		go func(i int, generator *Generator) {
			defer wg.Done()
			results[i] = generator.AddTemplate(sources[i])
		}(i, generator)
	}
	wg.Wait()
	if !(results[0] == nil && errors.Is(results[1], ErrTemplateAlreadyExists) ||
		results[1] == nil && errors.Is(results[0], ErrTemplateAlreadyExists)) {
		t.Fatalf("wanted one move and one conflict, got %v", results)
	}
	for i, result := range results {
		_, err := os.Lstat(sources[i])
		if result == nil && !errors.Is(err, os.ErrNotExist) || result != nil && err != nil {
			t.Fatalf("source %d in wrong state after %v: %v", i, result, err)
		}
	}
	winner := 0
	if results[1] == nil {
		winner = 1
	}
	content, err := os.ReadFile(filepath.Join(configDir, "templates", "shared.md"))
	if err != nil || len(content) != 1 || content[0] != byte('a'+winner) {
		t.Fatalf("unexpected winning template %q: %v", content, err)
	}
}

func TestGeneratorLoadTemplate(t *testing.T) {
	t.Run("should load template", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()

		generator, err := NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := "custom template"
		err = os.WriteFile(filepath.Join(generator.templatesDir, "custom.md"), []byte(want), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := generator.LoadTemplate("custom.md")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})

	t.Run("should trim template name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		configDir := t.TempDir()

		generator, err := NewGenerator(repo, WithConfigDir(configDir))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := "custom template"
		err = os.WriteFile(filepath.Join(generator.templatesDir, "custom.md"), []byte(want), 0600)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := generator.LoadTemplate(" custom.md ")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})

	t.Run("should raise an error if template name is empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate(" ")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name is absolute path", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate("/tmp/custom.md")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name contains parent directory reference", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate("../custom.md")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name contains subdirectory", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate("nested/custom.md")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template does not exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate("missing.md")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
	t.Run("should reject parent directory reference after trimming", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.LoadTemplate(" ../evil.md ")
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
}

func TestGeneratorSaveTemplate(t *testing.T) {
	t.Run("should save template", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []byte("custom template")

		err = generator.SaveTemplate("custom.md", want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := os.ReadFile(filepath.Join(generator.templatesDir, "custom.md"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != string(want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", string(want), string(got))
		}
	})

	t.Run("should trim template name", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []byte("custom template")

		err = generator.SaveTemplate(" custom.md ", want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := os.ReadFile(filepath.Join(generator.templatesDir, "custom.md"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != string(want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", string(want), string(got))
		}
	})

	t.Run("should overwrite existing template", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate("custom.md", []byte("old template"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := []byte("new template")

		err = generator.SaveTemplate("custom.md", want)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := os.ReadFile(filepath.Join(generator.templatesDir, "custom.md"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if string(got) != string(want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", string(want), string(got))
		}
	})

	t.Run("should raise an error if template name is empty", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate(" ", []byte("template"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name is absolute path", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate("/tmp/custom.md", []byte("template"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name contains parent directory reference", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate("../custom.md", []byte("template"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template name contains subdirectory", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate("nested/custom.md", []byte("template"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
	t.Run("should raise an error if templates directory does not exist", func(t *testing.T) {

		repo, teardown := setupTestDB(t)
		defer teardown()
		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		generator.templatesDir = filepath.Join(t.TempDir(), "missing")
		err = generator.SaveTemplate("test.md", []byte("test"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}

	})
	t.Run("should reject parent directory reference after trimming", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo, WithConfigDir(t.TempDir()))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.SaveTemplate(" ../evil.md ", []byte("test"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
}
func TestGeneratorFuncMap(t *testing.T) {
	t.Run("should count findings by severity", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["severityCount"].(func([]*domain.Finding) map[string]int)

		got := fn([]*domain.Finding{
			{Severity: "Critical"},
			{Severity: "High"},
			{Severity: "High"},
			{Severity: "Informational"},
			{Severity: "Unknown"},
			nil,
		})

		want := map[string]int{
			"Critical":      1,
			"High":          2,
			"Medium":        0,
			"Low":           0,
			"Informational": 1,
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should increment integer", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["inc"].(func(int) int)

		got := fn(1)
		want := 2

		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should uppercase string", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["upper"].(func(string) string)

		got := fn("high")
		want := "HIGH"

		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should not truncate input shorter than max length", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["truncate"].(func([]byte, int) []byte)

		want := []byte("hello")
		got := fn(want, 10)

		if string(got) != string(want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", string(want), string(got))
		}
	})

	t.Run("should truncate input longer than max length", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["truncate"].(func([]byte, int) []byte)

		got := fn([]byte("hello world"), 5)
		want := "hello\n\n[... TRUNCATED ...]"

		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})

	t.Run("should not truncate when max length is zero", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["truncate"].(func([]byte, int) []byte)

		want := []byte("hello world")
		got := fn(want, 0)

		if string(got) != string(want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", string(want), string(got))
		}
	})

	t.Run("should sort findings by severity and cvss score", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["sortFindings"].(func([]*domain.Finding) []*domain.Finding)

		low := &domain.Finding{Title: "low", Severity: "Low", CVSSScore: 3.1}
		highLowScore := &domain.Finding{Title: "high low score", Severity: "High", CVSSScore: 7.1}
		highHighScore := &domain.Finding{Title: "high high score", Severity: "High", CVSSScore: 8.4}
		critical := &domain.Finding{Title: "critical", Severity: "Critical", CVSSScore: 9.1}

		got := fn([]*domain.Finding{
			low,
			highLowScore,
			critical,
			highHighScore,
		})

		want := []*domain.Finding{
			critical,
			highHighScore,
			highLowScore,
			low,
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should identify image artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["isImage"].(func(*domain.ArtifactMetadata) bool)

		got := fn(&domain.ArtifactMetadata{
			MimeType: "image/png",
		})
		want := true

		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should reject non image artifacts", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["isImage"].(func(*domain.ArtifactMetadata) bool)

		got := fn(&domain.ArtifactMetadata{
			MimeType: "text/plain",
		})
		want := false

		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})

	t.Run("should clean non printable bytes", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["cleanPrint"].(func([]byte) string)

		got := fn([]byte{'A', 0x00, '\n', '\t', 0xff})
		want := "A\\x00\n\t\\xFF"

		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
	t.Run("should get row from repository", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id := testRequest(t, repo, nil)
		insertTestResponseAndGet(t, repo, id, nil)

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["getRow"].(func(uuid.UUID) (*domain.RequestResponseRow, error))

		got, err := fn(id)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.Request.ID != id {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", id, got.Request.ID)
		}

		if got.Response.ID != id {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", id, got.Response.ID)
		}
	})
	t.Run("should get rows from repository", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		id := testRequest(t, repo, nil)
		insertTestResponseAndGet(t, repo, id, nil)

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["getRows"].(func([]uuid.UUID) ([]*domain.RequestResponseRow, error))

		got, err := fn([]uuid.UUID{id})
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", 1, len(got))
		}

		if got[0].Request.ID != id {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", id, got[0].Request.ID)
		}

		if got[0].Response.ID != id {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", id, got[0].Response.ID)
		}
	})
	t.Run("should get websocket transcript from repository", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		requestID := testRequest(t, repo, map[string]any{"protocol": "websocket"})
		connectionID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		connection := &domain.WebSocketConnection{
			ID:        connectionID,
			RequestID: requestID,
			State:     "open",
			Transport: "wss",
			Host:      "marasi.app",
			Path:      "/ws",
			StartedAt: time.Now(),
		}
		if err := repo.InsertConnection(connection); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		messageID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		message := &domain.WebSocketMessage{
			ID:           messageID,
			ConnectionID: connectionID,
			RequestID:    requestID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte("hello"),
			CreatedAt:    time.Now(),
			Metadata:     map[string]any{},
		}
		if err := repo.InsertMessage(message); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["getWebSocket"].(func(uuid.UUID) (*WebSocketTranscript, error))
		got, err := fn(requestID)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if got.Connection.ID != connectionID {
			t.Fatalf("\nwanted:\n%s\ngot:\n%s", connectionID, got.Connection.ID)
		}
		if len(got.Messages) != 1 || got.Messages[0].ID != messageID {
			t.Fatalf("\nwanted:\n%s\ngot:\n%#v", messageID, got.Messages)
		}
	})
	t.Run("should return artifact data uri", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: nil,
			Title:      "Artifact Parent",
			Severity:   "High",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:        artID,
				Filename:  "marasi_finding.png",
				MimeType:  "image/png",
				Size:      4,
				CreatedAt: time.Now(),
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["artifactDataURI"].(func(*domain.ArtifactMetadata) (string, error))

		got, err := fn(art.ArtifactMetadata)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := "data:image/png;base64,ZGF0YQ=="
		if got != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
	t.Run("should raise an error if artifact is not an image", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		fID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		finding := &domain.Finding{
			ID:         fID,
			TestCaseID: nil,
			Title:      "Artifact Parent",
			Severity:   "High",
			WriteUp:    "",
			Requests:   []uuid.UUID{},
			Artifacts:  []*domain.ArtifactMetadata{},
		}

		err = repo.SaveFinding(finding)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		artID, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		art := &domain.Artifact{
			ArtifactMetadata: &domain.ArtifactMetadata{
				ID:        artID,
				Filename:  "notes.txt",
				MimeType:  "text/plain",
				Size:      4,
				CreatedAt: time.Now(),
			},
			TestCaseID: nil,
			FindingID:  &fID,
			Data:       []byte("data"),
		}

		err = repo.SaveArtifact(art)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		fn := generator.FuncMap()["artifactDataURI"].(func(*domain.ArtifactMetadata) (string, error))

		_, err = fn(art.ArtifactMetadata)
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
}

func TestGeneratorExecute(t *testing.T) {
	t.Run("should execute template", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		payload := &domain.ReportPayload{
			Metadata: domain.ReportMetadata{
				Title:  "Test Report",
				Client: "Test Client",
			},
		}

		got, err := generator.Execute([]byte("{{.Metadata.Title}} - {{.Metadata.Client}}"), payload)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := "Test Report - Test Client"
		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})

	t.Run("should raise an error if template cannot parse", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.Execute([]byte("{{.Metadata.Title"), &domain.ReportPayload{})
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})

	t.Run("should raise an error if template references missing key", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		_, err = generator.Execute([]byte("{{.Metadata.MissingField}}"), &domain.ReportPayload{})
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
	t.Run("should execute template with function map", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		payload := &domain.ReportPayload{
			Metadata: domain.ReportMetadata{
				Title: "test report",
			},
		}

		got, err := generator.Execute([]byte(`{{upper .Metadata.Title}}`), payload)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		want := "TEST REPORT"
		if string(got) != want {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, string(got))
		}
	})
}

func TestDefaultTemplateWebSocketTranscript(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()

	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	metadata := map[string]any{
		"protocol":            "websocket",
		"websocket.transport": "wss",
		"websocket.state":     "closed",
	}
	requestID := testRequest(t, repo, metadata)

	response := &domain.ProxyResponse{
		ID:          requestID,
		Status:      "101 Switching Protocols",
		StatusCode:  101,
		Raw:         []byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"),
		Metadata:    metadata,
		RespondedAt: now,
	}
	if err := repo.InsertResponse(response); err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	connectionID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	closedAt := now.Add(time.Minute)
	connection := &domain.WebSocketConnection{
		ID:          connectionID,
		RequestID:   requestID,
		State:       "closed",
		Transport:   "wss",
		Host:        "marasi.app",
		Path:        "/ws?room=test",
		StartedAt:   now,
		ClosedAt:    &closedAt,
		CloseCode:   1000,
		CloseReason: "complete",
	}
	if err := repo.InsertConnection(connection); err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	failedUpgradeID := testRequest(t, repo, metadata)
	insertTestResponseAndGet(t, repo, failedUpgradeID, metadata)

	for _, message := range []*domain.WebSocketMessage{
		{
			ConnectionID: connectionID,
			RequestID:    requestID,
			Direction:    "client",
			Opcode:       1,
			Fin:          true,
			Payload:      []byte(`{"message":"hello"}`),
			CreatedAt:    now.Add(time.Second),
			Metadata: map[string]any{
				"dropped":  true,
				"injected": true,
			},
		},
		{
			ConnectionID: connectionID,
			RequestID:    requestID,
			Direction:    "server",
			Opcode:       2,
			Fin:          true,
			Payload:      []byte{0x00, 0xff},
			IsBinary:     true,
			CreatedAt:    now.Add(2 * time.Second),
			Metadata: map[string]any{
				"generated": true,
			},
		},
	} {
		message.ID, err = uuid.NewV7()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if err := repo.InsertMessage(message); err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	}

	generator, err := NewGenerator(repo)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	payload := &domain.ReportPayload{
		Metadata: domain.ReportMetadata{
			Title:          "WebSocket Assessment",
			Client:         "Test Client",
			Type:           "Assessment",
			Scope:          "marasi.app",
			Assessor:       "Tester",
			Start:          now,
			End:            now.Add(time.Hour),
			CreatedAt:      now,
			TruncateLength: 0,
		},
		Findings: []*domain.Finding{
			{
				Title:         "WebSocket authorization bypass",
				Severity:      "High",
				Requests:      []uuid.UUID{requestID, failedUpgradeID},
				Artifacts:     []*domain.ArtifactMetadata{},
				WriteUp:       "WebSocket finding",
				TreatmentPlan: "Authorize messages",
			},
		},
		TestCases: []*domain.TestCase{
			{
				Title:       "WebSocket authorization test",
				Description: "Verify WebSocket message authorization.",
				Category:    "Authorization",
				Tags:        []string{"websocket", "access-control"},
				Requests:    []uuid.UUID{requestID, failedUpgradeID},
				Note:        "Test both allowed and denied messages.",
			},
		},
	}

	out, err := generator.Execute([]byte(defaultTemplate), payload)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}

	for _, want := range []string{
		"WebSocket Connection",
		"wss://marasi.app/ws?room=test",
		"1000 complete",
		"# Test Cases",
		"## TC-01 WebSocket authorization test",
		"Verify WebSocket message authorization.",
		"websocket, access-control",
		"[Appendix A: WebSocket Messages](#appendix-finding-1-request-1)",
		"[Appendix A: WebSocket Messages](#appendix-test-case-1-request-1)",
		"# Appendix A: WebSocket Messages",
		`<a id="appendix-finding-1-request-1"></a>`,
		`<a id="appendix-test-case-1-request-1"></a>`,
		"## Finding 1 - Request 1",
		"## Test Case 1 - Request 1",
		"### ~~Message 1~~",
		`{"message":"hello"}`,
		`\x00\xFF`,
		"```text",
		"#### Metadata",
		`"dropped": true`,
		`"generated": true`,
		`dropped, injected`,
		`| server | 2 | true |  |`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered report does not contain %q:\n%s", want, out)
		}
	}
	if count := strings.Count(string(out), "# Appendix A: WebSocket Messages"); count != 1 {
		t.Errorf("rendered report contains %d appendix headings, wanted 1:\n%s", count, out)
	}
	if strings.Contains(string(out), "&#34;") {
		t.Errorf("rendered report HTML-escapes websocket payloads:\n%s", out)
	}
}

func TestGeneratorValidate(t *testing.T) {
	t.Run("should validate template", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.Validate([]byte("{{.Metadata.Title}}"))
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
	})

	t.Run("should raise an error if template is invalid", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		generator, err := NewGenerator(repo)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = generator.Validate([]byte("{{.Metadata.Title"))
		if err == nil {
			t.Fatal("\nwanted:\nerr\ngot:\nnil")
		}
	})
}
