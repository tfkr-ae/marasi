package service

import (
	_ "embed"
	"fmt"
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

func addTestCaseChecklistRoute(mux *http.ServeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("GET /test-case/checklist", func(w http.ResponseWriter, r *http.Request) {
		checklist, err := loadTestCaseChecklist(proxy.ConfigDir)
		if err != nil {
			writeTestCaseError(w, r, http.StatusInternalServerError, "internal_server_error")
			return
		}
		writeJSON(w, r, http.StatusOK, checklist)
	})
}

func loadTestCaseChecklist(configDir string) (testCaseChecklist, error) {
	path := filepath.Join(configDir, "test_cases.yml")
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return testCaseChecklist{}, fmt.Errorf("checking test case checklist: %w", err)
		}
		if err := os.WriteFile(path, defaultTestCasesYAML, 0600); err != nil {
			return testCaseChecklist{}, fmt.Errorf("writing default test case checklist: %w", err)
		}
	}

	config := viper.New()
	config.SetConfigFile(path)
	config.SetConfigType("yaml")
	if err := config.ReadInConfig(); err != nil {
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
