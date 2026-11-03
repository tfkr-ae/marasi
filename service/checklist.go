package service

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"github.com/tfkr-ae/marasi"
)

//go:embed default_test_cases.yml
var defaultTestCasesYAML []byte

type testCaseChecklistItem struct {
	Title       string `json:"title" mapstructure:"title"`
	Description string `json:"description" mapstructure:"description"`
	Category    string `json:"category" mapstructure:"category"`
}

type testCaseChecklist struct {
	Title       string                  `json:"title" mapstructure:"title"`
	Description string                  `json:"description" mapstructure:"description"`
	Version     string                  `json:"version" mapstructure:"version"`
	Items       []testCaseChecklistItem `json:"items" mapstructure:"test_cases"`
}

func addTestCaseChecklistRoute(mux routeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /test-case/checklist", func(w http.ResponseWriter, r *http.Request) {
		checklist, err := loadTestCaseChecklist(proxy.ConfigDir)
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, checklist)
	})
}

// testCaseChecklistFile is the checklist a user edits, in the config dir.
const testCaseChecklistFile = "test_cases.yml"

// WriteDefaultTestCaseChecklist writes the bundled checklist to the config
// dir when no checklist is there, so a user has a file to edit. The service
// calls it at start rather than on GET /test-case/checklist, because a GET
// changes no state (docs/adr/2026-11-03-the-control-listener-network-address-is-guarded-by-an-optional-password.md).
func WriteDefaultTestCaseChecklist(configDir string) error {
	file, err := os.OpenFile(filepath.Join(configDir, testCaseChecklistFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("writing default test case checklist: %w", err)
	}
	_, err = file.Write(defaultTestCasesYAML)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("writing default test case checklist: %w", err)
	}
	return nil
}

// loadTestCaseChecklist reads the config dir's checklist, or the bundled one
// when that file is missing. It writes nothing.
func loadTestCaseChecklist(configDir string) (testCaseChecklist, error) {
	content, err := os.ReadFile(filepath.Join(configDir, testCaseChecklistFile))
	if errors.Is(err, fs.ErrNotExist) {
		content = defaultTestCasesYAML
	} else if err != nil {
		return testCaseChecklist{}, fmt.Errorf("reading test case checklist: %w", err)
	}

	config := viper.New()
	config.SetConfigType("yaml")
	if err := config.ReadConfig(bytes.NewReader(content)); err != nil {
		return testCaseChecklist{}, fmt.Errorf("reading test case checklist: %w", err)
	}
	var checklist testCaseChecklist
	if err := config.Unmarshal(&checklist); err != nil {
		return testCaseChecklist{}, fmt.Errorf("decoding test case checklist: %w", err)
	}
	if checklist.Items == nil {
		checklist.Items = []testCaseChecklistItem{}
	}
	return checklist, nil
}
