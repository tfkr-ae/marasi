package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/service"
)

var findingCreateTitle string
var findingCreateSeverity string
var findingCreateCVSSVector string
var findingCreateCVSSScore float64
var findingCreateWriteUp string
var findingCreateTreatmentPlan string
var findingCreateTestCase string
var findingUpdateTitle string
var findingUpdateSeverity string
var findingUpdateCVSSVector string
var findingUpdateCVSSScore float64
var findingUpdateWriteUp string
var findingUpdateTreatmentPlan string
var findingUpdateTestCase string
var findingUpdateClearTestCase bool
var findingLinkRequest string
var findingUnlinkRequest string

type findingRequest struct {
	Title         *string  `json:"title,omitempty"`
	Severity      *string  `json:"severity,omitempty"`
	CVSSVector    *string  `json:"cvss_vector,omitempty"`
	CVSSScore     *float64 `json:"cvss_score,omitempty"`
	WriteUp       *string  `json:"writeup,omitempty"`
	TreatmentPlan *string  `json:"treatment_plan,omitempty"`
	TestCaseID    **string `json:"test_case_id,omitempty"`
}

func init() {
	findingCreateCmd.Flags().StringVar(&findingCreateTitle, "title", "", "Finding title")
	findingCreateCmd.Flags().StringVar(&findingCreateSeverity, "severity", "", "Finding severity")
	findingCreateCmd.Flags().StringVar(&findingCreateCVSSVector, "cvss-vector", "", "Finding CVSS vector")
	findingCreateCmd.Flags().Float64Var(&findingCreateCVSSScore, "cvss-score", 0, "Finding CVSS score")
	findingCreateCmd.Flags().StringVar(&findingCreateWriteUp, "writeup", "", "Finding writeup")
	findingCreateCmd.Flags().StringVar(&findingCreateTreatmentPlan, "treatment-plan", "", "Finding treatment plan")
	findingCreateCmd.Flags().StringVar(&findingCreateTestCase, "test-case", "", "Related test case UUID")
	findingCreateCmd.MarkFlagRequired("title")
	findingUpdateCmd.Flags().StringVar(&findingUpdateTitle, "title", "", "Finding title")
	findingUpdateCmd.Flags().StringVar(&findingUpdateSeverity, "severity", "", "Finding severity")
	findingUpdateCmd.Flags().StringVar(&findingUpdateCVSSVector, "cvss-vector", "", "Finding CVSS vector")
	findingUpdateCmd.Flags().Float64Var(&findingUpdateCVSSScore, "cvss-score", 0, "Finding CVSS score")
	findingUpdateCmd.Flags().StringVar(&findingUpdateWriteUp, "writeup", "", "Finding writeup")
	findingUpdateCmd.Flags().StringVar(&findingUpdateTreatmentPlan, "treatment-plan", "", "Finding treatment plan")
	findingUpdateCmd.Flags().StringVar(&findingUpdateTestCase, "test-case", "", "Related test case UUID")
	findingUpdateCmd.Flags().BoolVar(&findingUpdateClearTestCase, "clear-test-case", false, "Clear the related test case")
	findingLinkCmd.Flags().StringVar(&findingLinkRequest, "request", "", "Request UUID")
	findingLinkCmd.MarkFlagRequired("request")
	findingUnlinkCmd.Flags().StringVar(&findingUnlinkRequest, "request", "", "Request UUID")
	findingUnlinkCmd.MarkFlagRequired("request")
	findingCmd.AddCommand(findingCreateCmd, findingListCmd, findingGetCmd, findingUpdateCmd, findingDeleteCmd, findingLinkCmd, findingUnlinkCmd)
	rootCmd.AddCommand(findingCmd)
}

var findingCmd = &cobra.Command{
	Use:   "finding",
	Short: "Manage findings for a service instance",
}

var findingCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a finding",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if findingCreateTitle == "" {
			return errors.New("finding create requires a non-empty --title")
		}
		request := findingRequest{Title: &findingCreateTitle}
		applyFindingCreateFlags(cmd, &request)
		return runFindingCommand(cmd, "create", "", request)
	},
}

var findingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List findings newest-first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runFindingCommand(cmd, "list", "", findingRequest{})
	},
}

var findingGetCmd = &cobra.Command{
	Use:   "get uuid",
	Short: "Get one finding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFindingCommand(cmd, "get", args[0], findingRequest{})
	},
}

var findingUpdateCmd = &cobra.Command{
	Use:   "update uuid",
	Short: "Update a finding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("test-case") && findingUpdateClearTestCase {
			return errors.New("finding update accepts only one of --test-case and --clear-test-case")
		}
		var request findingRequest
		if cmd.Flags().Changed("title") {
			if findingUpdateTitle == "" {
				return errors.New("finding update requires a non-empty --title")
			}
			request.Title = &findingUpdateTitle
		}
		if cmd.Flags().Changed("severity") {
			request.Severity = &findingUpdateSeverity
		}
		if cmd.Flags().Changed("cvss-vector") {
			request.CVSSVector = &findingUpdateCVSSVector
		}
		if cmd.Flags().Changed("cvss-score") {
			request.CVSSScore = &findingUpdateCVSSScore
		}
		if cmd.Flags().Changed("writeup") {
			request.WriteUp = &findingUpdateWriteUp
		}
		if cmd.Flags().Changed("treatment-plan") {
			request.TreatmentPlan = &findingUpdateTreatmentPlan
		}
		if cmd.Flags().Changed("test-case") {
			value := &findingUpdateTestCase
			request.TestCaseID = &value
		}
		if findingUpdateClearTestCase {
			var value *string
			request.TestCaseID = &value
		}
		if request.Title == nil && request.Severity == nil && request.CVSSVector == nil && request.CVSSScore == nil && request.WriteUp == nil && request.TreatmentPlan == nil && request.TestCaseID == nil {
			return errors.New("finding update requires at least one changed flag")
		}
		return runFindingCommand(cmd, "update", args[0], request)
	},
}

var findingDeleteCmd = &cobra.Command{
	Use:   "delete uuid",
	Short: "Delete a finding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFindingCommand(cmd, "delete", args[0], findingRequest{})
	},
}

var findingLinkCmd = &cobra.Command{
	Use:   "link uuid",
	Short: "Link traffic to a finding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficMembershipCommand(cmd, "finding", args[0], findingLinkRequest, true)
	},
}

var findingUnlinkCmd = &cobra.Command{
	Use:   "unlink uuid",
	Short: "Unlink traffic from a finding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficMembershipCommand(cmd, "finding", args[0], findingUnlinkRequest, false)
	},
}

func applyFindingCreateFlags(cmd *cobra.Command, request *findingRequest) {
	if cmd.Flags().Changed("severity") {
		request.Severity = &findingCreateSeverity
	}
	if cmd.Flags().Changed("cvss-vector") {
		request.CVSSVector = &findingCreateCVSSVector
	}
	if cmd.Flags().Changed("cvss-score") {
		request.CVSSScore = &findingCreateCVSSScore
	}
	if cmd.Flags().Changed("writeup") {
		request.WriteUp = &findingCreateWriteUp
	}
	if cmd.Flags().Changed("treatment-plan") {
		request.TreatmentPlan = &findingCreateTreatmentPlan
	}
	if cmd.Flags().Changed("test-case") {
		value := &findingCreateTestCase
		request.TestCaseID = &value
	}
}

func runFindingCommand(cmd *cobra.Command, action, id string, payload findingRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	response, err := controlFinding(ctx, instancePath, instance, jsonOutput, action, id, payload)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	if action == "list" {
		return writeFindingListHuman(response, cmd.OutOrStdout())
	}
	if action == "get" {
		return writeFindingGetHuman(response, cmd.OutOrStdout())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.ID == "" {
		return errors.New("decoding finding response")
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "finding %s %sd successfully\n", result.ID, action)
	return err
}

func controlFinding(ctx context.Context, instancePath, instanceName string, asJSON bool, action, id string, payload findingRequest) ([]byte, error) {
	method := http.MethodGet
	path := "/finding"
	operation := "listing findings"
	var requestBody io.Reader
	switch action {
	case "create":
		method, operation = http.MethodPost, "creating finding"
	case "get":
		path, operation = path+"/"+id, "getting finding"
	case "update":
		method, path, operation = http.MethodPost, path+"/"+id, "updating finding"
	case "delete":
		method, path, operation = http.MethodDelete, path+"/"+id, "deleting finding"
	}
	if method == http.MethodPost {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding finding request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating finding request: %w", err)
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
		return nil, errors.Join(wrapError("reading finding response", readErr), wrapError("closing finding response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return nil, controlAPIError(operation, response.Status, body)
		}
		return nil, fmt.Errorf("%s: %s", operation, response.Status)
	}
	return body, nil
}

func writeFindingListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Severity   string  `json:"severity"`
			TestCaseID *string `json:"test_case_id"`
			CVSSScore  float64 `json:"cvss_score"`
			CreatedAt  string  `json:"created_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding finding list: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%g\t%s\n", item.ID, item.Title, item.Severity, findingTestCaseText(item.TestCaseID), item.CVSSScore, item.CreatedAt)
	}
	return writer.Flush()
}

func writeFindingGetHuman(body []byte, stdout io.Writer) error {
	var response struct {
		ID            string  `json:"id"`
		TestCaseID    *string `json:"test_case_id"`
		Title         string  `json:"title"`
		Severity      string  `json:"severity"`
		CVSSVector    string  `json:"cvss_vector"`
		CVSSScore     float64 `json:"cvss_score"`
		WriteUp       string  `json:"writeup"`
		TreatmentPlan string  `json:"treatment_plan"`
		CreatedAt     string  `json:"created_at"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding finding: %w", err)
	}
	_, err := fmt.Fprintf(stdout, "id: %s\ntest_case_id: %s\ntitle: %s\nseverity: %s\ncvss_vector: %s\ncvss_score: %g\nwriteup: %s\ntreatment_plan: %s\ncreated_at: %s\n", response.ID, findingTestCaseText(response.TestCaseID), response.Title, response.Severity, response.CVSSVector, response.CVSSScore, response.WriteUp, response.TreatmentPlan, response.CreatedAt)
	return err
}

func findingTestCaseText(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}
