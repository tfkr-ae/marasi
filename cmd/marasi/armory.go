package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/service"
)

var armoryTemplateCreateName string
var armoryTemplateCreateDescription string
var armoryTemplateCreateRawFile string
var armoryTemplateUpdateName string
var armoryTemplateUpdateDescription string
var armoryTemplateUpdateRawFile string
var armoryRunTemplate string
var armoryRunAttackType string
var armoryRunWordlists []string
var armoryRunHTTP bool
var armoryRunMaxConcurrent int
var armoryRunRawFile string
var armoryRunTrafficLimit string
var armoryRunTrafficCursor string

type armoryTemplateRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	RawTemplate *string `json:"raw_template,omitempty"`
}

type armoryRunRequest struct {
	TemplateID    *string                 `json:"template_id,omitempty"`
	RawTemplate   *string                 `json:"raw_template,omitempty"`
	AttackType    domain.ArmoryAttackType `json:"attack_type"`
	Wordlists     []string                `json:"wordlists"`
	UseHTTPS      *bool                   `json:"use_https,omitempty"`
	MaxConcurrent *int                    `json:"max_concurrent,omitempty"`
}

type armoryRunResponse struct {
	ID            string                  `json:"id"`
	TemplateID    string                  `json:"template_id"`
	Status        string                  `json:"status"`
	AttackType    domain.ArmoryAttackType `json:"attack_type"`
	UseHTTPS      bool                    `json:"use_https"`
	MaxConcurrent int                     `json:"max_concurrent"`
	Wordlists     []string                `json:"wordlists"`
	CreatedAt     string                  `json:"created_at"`
	StartedAt     *string                 `json:"started_at"`
	FinishedAt    *string                 `json:"finished_at"`
}

func init() {
	armoryTemplateCreateCmd.Flags().StringVar(&armoryTemplateCreateName, "name", "", "Template name")
	armoryTemplateCreateCmd.Flags().StringVar(&armoryTemplateCreateDescription, "description", "", "Template description")
	armoryTemplateCreateCmd.Flags().StringVar(&armoryTemplateCreateRawFile, "raw-file", "", "Read the raw template from a file")
	armoryTemplateCreateCmd.MarkFlagRequired("name")
	armoryTemplateUpdateCmd.Flags().StringVar(&armoryTemplateUpdateName, "name", "", "Template name")
	armoryTemplateUpdateCmd.Flags().StringVar(&armoryTemplateUpdateDescription, "description", "", "Template description")
	armoryTemplateUpdateCmd.Flags().StringVar(&armoryTemplateUpdateRawFile, "raw-file", "", "Read the raw template from a file")
	armoryRunListCmd.Flags().StringVar(&armoryRunTemplate, "template", "", "Template UUID")
	armoryRunListCmd.MarkFlagRequired("template")
	addArmoryRunFlags(armoryRunCreateCmd, true)
	addArmoryRunFlags(armoryRunValidateCmd, false)
	armoryRunValidateCmd.Flags().StringVar(&armoryRunRawFile, "raw-file", "", "Read the raw template from a file")
	armoryRunTrafficCmd.Flags().StringVar(&armoryRunTrafficLimit, "limit", "200", "Page size")
	armoryRunTrafficCmd.Flags().StringVar(&armoryRunTrafficCursor, "cursor", "", "Fetch the next page")
	armoryTemplateCmd.AddCommand(armoryTemplateListCmd, armoryTemplateGetCmd, armoryTemplateCreateCmd, armoryTemplateUpdateCmd, armoryTemplateDeleteCmd)
	armoryRunCmd.AddCommand(armoryRunListCmd, armoryRunGetCmd, armoryRunCreateCmd, armoryRunValidateCmd, armoryRunStartCmd, armoryRunCancelCmd, armoryRunDeleteCmd, armoryRunTrafficCmd)
	armoryCmd.AddCommand(armoryTemplateCmd, armoryRunCmd)
	rootCmd.AddCommand(armoryCmd)
}

func addArmoryRunFlags(cmd *cobra.Command, includeTemplate bool) {
	if includeTemplate {
		cmd.Flags().StringVar(&armoryRunTemplate, "template", "", "Template UUID")
		cmd.MarkFlagRequired("template")
	}
	cmd.Flags().StringVar(&armoryRunAttackType, "attack-type", "", "Attack type")
	cmd.Flags().StringArrayVar(&armoryRunWordlists, "wordlist", nil, "Wordlist name (repeatable)")
	cmd.Flags().BoolVar(&armoryRunHTTP, "http", false, "Use HTTP instead of HTTPS")
	cmd.Flags().IntVar(&armoryRunMaxConcurrent, "max-concurrent", 0, "Maximum concurrent requests")
	cmd.MarkFlagRequired("attack-type")
}

var armoryCmd = &cobra.Command{Use: "armory", Short: "Manage Armory templates and runs"}
var armoryTemplateCmd = &cobra.Command{Use: "template", Short: "Manage Armory templates"}
var armoryRunCmd = &cobra.Command{Use: "run", Short: "Manage Armory runs"}

var armoryTemplateListCmd = &cobra.Command{
	Use: "list", Short: "List Armory templates", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runArmoryCommand(cmd, http.MethodGet, "/armory/template", nil, "listing Armory templates", writeArmoryTemplateListHuman, "")
	},
}

var armoryTemplateGetCmd = &cobra.Command{
	Use: "get uuid", Short: "Get an Armory template", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodGet, "/armory/template/"+args[0], nil, "getting Armory template", writeArmoryTemplateHuman, "")
	},
}

var armoryTemplateCreateCmd = &cobra.Command{
	Use: "create", Short: "Create an Armory template", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(armoryTemplateCreateName) == "" {
			return errors.New("armory template create requires a non-empty --name")
		}
		request := armoryTemplateRequest{Name: &armoryTemplateCreateName}
		if cmd.Flags().Changed("description") {
			request.Description = &armoryTemplateCreateDescription
		}
		if cmd.Flags().Changed("raw-file") {
			raw, err := readArmoryRawFile(armoryTemplateCreateRawFile)
			if err != nil {
				return err
			}
			request.RawTemplate = &raw
		}
		return runArmoryCommand(cmd, http.MethodPost, "/armory/template", request, "creating Armory template", nil, "armory template %s created successfully\n")
	},
}

var armoryTemplateUpdateCmd = &cobra.Command{
	Use: "update uuid", Short: "Update an Armory template", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var request armoryTemplateRequest
		if cmd.Flags().Changed("name") {
			if strings.TrimSpace(armoryTemplateUpdateName) == "" {
				return errors.New("armory template update requires a non-empty --name")
			}
			request.Name = &armoryTemplateUpdateName
		}
		if cmd.Flags().Changed("description") {
			request.Description = &armoryTemplateUpdateDescription
		}
		if cmd.Flags().Changed("raw-file") {
			raw, err := readArmoryRawFile(armoryTemplateUpdateRawFile)
			if err != nil {
				return err
			}
			request.RawTemplate = &raw
		}
		if request.Name == nil && request.Description == nil && request.RawTemplate == nil {
			return errors.New("armory template update requires --name, --description, or --raw-file")
		}
		return runArmoryCommand(cmd, http.MethodPost, "/armory/template/"+args[0], request, "updating Armory template", nil, "armory template %s updated successfully\n")
	},
}

var armoryTemplateDeleteCmd = &cobra.Command{
	Use: "delete uuid", Short: "Delete an Armory template", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodDelete, "/armory/template/"+args[0], nil, "deleting Armory template", nil, "armory template %s deleted successfully\n")
	},
}

var armoryRunListCmd = &cobra.Command{
	Use: "list", Short: "List runs for an Armory template", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runArmoryCommand(cmd, http.MethodGet, "/armory/template/"+armoryRunTemplate+"/run", nil, "listing Armory runs", writeArmoryRunListHuman, "")
	},
}

var armoryRunGetCmd = &cobra.Command{
	Use: "get uuid", Short: "Get an Armory run", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodGet, "/armory/run/"+args[0], nil, "getting Armory run", writeArmoryRunHuman, "")
	},
}

var armoryRunCreateCmd = &cobra.Command{
	Use: "create", Short: "Create an Armory run", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		request, err := armoryRunFlagsRequest(cmd)
		if err != nil {
			return err
		}
		request.TemplateID = &armoryRunTemplate
		return runArmoryCommand(cmd, http.MethodPost, "/armory/run", request, "creating Armory run", nil, "armory run %s created successfully\n")
	},
}

var armoryRunValidateCmd = &cobra.Command{
	Use: "validate", Short: "Validate an Armory run", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		request, err := armoryRunFlagsRequest(cmd)
		if err != nil {
			return err
		}
		raw, err := readArmoryRaw(cmd)
		if err != nil {
			return err
		}
		request.RawTemplate = &raw
		return runArmoryCommand(cmd, http.MethodPost, "/armory/run/validate", request, "validating Armory run", writeArmoryValidateHuman, "")
	},
}

var armoryRunStartCmd = &cobra.Command{
	Use: "start uuid", Short: "Start an Armory run", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodPost, "/armory/run/"+args[0]+"/start", nil, "starting Armory run", nil, "armory run %s started successfully\n")
	},
}

var armoryRunCancelCmd = &cobra.Command{
	Use: "cancel uuid", Short: "Cancel an Armory run", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodPost, "/armory/run/"+args[0]+"/cancel", nil, "cancelling Armory run", nil, "armory run %s cancelled successfully\n")
	},
}

var armoryRunDeleteCmd = &cobra.Command{
	Use: "delete uuid", Short: "Delete an Armory run", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArmoryCommand(cmd, http.MethodDelete, "/armory/run/"+args[0], nil, "deleting Armory run", nil, "armory run %s deleted successfully\n")
	},
}

var armoryRunTrafficCmd = &cobra.Command{
	Use: "traffic uuid", Short: "List traffic for an Armory run", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := url.Values{"limit": {armoryRunTrafficLimit}}
		if armoryRunTrafficCursor != "" {
			query.Set("cursor", armoryRunTrafficCursor)
		}
		path := "/armory/run/" + args[0] + "/traffic?" + query.Encode()
		return runArmoryCommand(cmd, http.MethodGet, path, nil, "listing Armory run traffic", func(body []byte, stdout io.Writer) error {
			return writeTrafficListHuman(body, stdout, cmd.ErrOrStderr())
		}, "")
	},
}

func armoryRunFlagsRequest(cmd *cobra.Command) (armoryRunRequest, error) {
	attackType, ok := domain.ParseArmoryAttackType(armoryRunAttackType)
	if !ok {
		return armoryRunRequest{}, errors.New("--attack-type must be harpoon, broadside, tandem, or maelstrom")
	}
	request := armoryRunRequest{AttackType: attackType, Wordlists: armoryRunWordlists}
	if request.Wordlists == nil {
		request.Wordlists = []string{}
	}
	if cmd.Flags().Changed("http") {
		useHTTPS := !armoryRunHTTP
		request.UseHTTPS = &useHTTPS
	}
	if cmd.Flags().Changed("max-concurrent") {
		request.MaxConcurrent = &armoryRunMaxConcurrent
	}
	return request, nil
}

func readArmoryRaw(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("raw-file") {
		return readArmoryRawFile(armoryRunRawFile)
	}
	stdin := cmd.InOrStdin()
	if file, ok := stdin.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return "", fmt.Errorf("checking stdin: %w", err)
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return "", errors.New("armory run validate requires --raw-file or piped stdin")
		}
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading raw template from stdin: %w", err)
	}
	return string(raw), nil
}

func readArmoryRawFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading raw template file: %w", err)
	}
	return string(raw), nil
}

func runArmoryCommand(cmd *cobra.Command, method, path string, payload any, operation string, human func([]byte, io.Writer) error, successFormat string) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	body, err := controlArmoryRequest(ctx, instancePath, instance, jsonOutput, method, path, payload, operation)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	if human != nil {
		return human(body, cmd.OutOrStdout())
	}
	if successFormat != "" {
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.ID == "" {
			return errors.New("decoding Armory response")
		}
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), successFormat, result.ID)
	}
	return err
}

func controlArmoryRequest(ctx context.Context, instancePath, instanceName string, asJSON bool, method, path string, payload any, operation string) ([]byte, error) {
	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding Armory request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating Armory request: %w", err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := service.NewClient(instancePath + ".sock")
	defer client.Close()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instance %s is not running", instanceName)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading Armory response", readErr), wrapError("closing Armory response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return nil, controlAPIError(operation, response.Status, body)
		}
		return nil, fmt.Errorf("%s: %s", operation, response.Status)
	}
	return body, nil
}

func writeArmoryTemplateListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding Armory template list: %w", err)
	}
	for index, item := range response.Items {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "id: %s\nname: %s\n", item.ID, item.Name)
	}
	return nil
}

func writeArmoryTemplateHuman(body []byte, stdout io.Writer) error {
	var response struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		RawTemplate string `json:"raw_template"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding Armory template: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "id: %s\nname: %s\ndescription: %s\n", response.ID, response.Name, response.Description); err != nil {
		return err
	}
	_, err := io.WriteString(stdout, response.RawTemplate)
	return err
}

func writeArmoryRunListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []armoryRunResponse `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding Armory run list: %w", err)
	}
	for index, run := range response.Items {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		if err := writeArmoryRunFields(stdout, run); err != nil {
			return err
		}
	}
	return nil
}

func writeArmoryRunHuman(body []byte, stdout io.Writer) error {
	var run armoryRunResponse
	if err := json.Unmarshal(body, &run); err != nil {
		return fmt.Errorf("decoding Armory run: %w", err)
	}
	return writeArmoryRunFields(stdout, run)
}

func writeArmoryRunFields(stdout io.Writer, run armoryRunResponse) error {
	_, err := fmt.Fprintf(stdout, "id: %s\ntemplate_id: %s\nstatus: %s\nattack_type: %s\nuse_https: %t\nmax_concurrent: %d\nwordlists: %s\ncreated_at: %s\nstarted_at: %s\nfinished_at: %s\n", run.ID, run.TemplateID, run.Status, run.AttackType, run.UseHTTPS, run.MaxConcurrent, strings.Join(run.Wordlists, ", "), run.CreatedAt, optionalString(run.StartedAt), optionalString(run.FinishedAt))
	return err
}

func writeArmoryValidateHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Status == "" {
		return errors.New("decoding Armory validation response")
	}
	_, err := fmt.Fprintf(stdout, "status: %s\n", response.Status)
	return err
}
