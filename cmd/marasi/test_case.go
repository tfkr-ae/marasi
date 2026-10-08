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
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var testCaseCreateTitle string
var testCaseCreateDescription string
var testCaseCreateCategory string
var testCaseCreateTags []string
var testCaseCreateNote string
var testCaseUpdateTitle string
var testCaseUpdateDescription string
var testCaseUpdateCategory string
var testCaseUpdateTags []string
var testCaseUpdateNote string
var testCaseLinkRequest string
var testCaseUnlinkRequest string

type testCaseRequest struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Category    *string   `json:"category,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	Note        *string   `json:"note,omitempty"`
}

func init() {
	testCaseCreateCmd.Flags().StringVar(&testCaseCreateTitle, "title", "", "Test case title. Required")
	testCaseCreateCmd.Flags().StringVar(&testCaseCreateDescription, "description", "", "Test case description")
	testCaseCreateCmd.Flags().StringVar(&testCaseCreateCategory, "category", "", "Test case category")
	testCaseCreateCmd.Flags().StringSliceVar(&testCaseCreateTags, "tag", nil, "Tag. Repeat the flag to set more than one")
	testCaseCreateCmd.Flags().StringVar(&testCaseCreateNote, "note", "", "Test case note")
	testCaseCreateCmd.MarkFlagRequired("title")
	testCaseUpdateCmd.Flags().StringVar(&testCaseUpdateTitle, "title", "", "Test case title")
	testCaseUpdateCmd.Flags().StringVar(&testCaseUpdateDescription, "description", "", "Test case description")
	testCaseUpdateCmd.Flags().StringVar(&testCaseUpdateCategory, "category", "", "Test case category")
	testCaseUpdateCmd.Flags().StringSliceVar(&testCaseUpdateTags, "tag", nil, "Tag. Repeat the flag to set more than one")
	testCaseUpdateCmd.Flags().StringVar(&testCaseUpdateNote, "note", "", "Test case note")
	testCaseLinkCmd.Flags().StringVar(&testCaseLinkRequest, "request", "", "UUID of the request/response pair")
	testCaseLinkCmd.MarkFlagRequired("request")
	testCaseUnlinkCmd.Flags().StringVar(&testCaseUnlinkRequest, "request", "", "UUID of the request/response pair")
	testCaseUnlinkCmd.MarkFlagRequired("request")
	testCaseCmd.AddCommand(testCaseCreateCmd, testCaseListCmd, testCaseGetCmd, testCaseUpdateCmd, testCaseDeleteCmd, testCaseLinkCmd, testCaseUnlinkCmd, testCaseChecklistCmd, testCaseListRequestsCmd)
	rootCmd.AddCommand(testCaseCmd)
}

var testCaseCmd = &cobra.Command{
	Use:   "test-case",
	Short: "Create, list, and link test cases",
}

var testCaseListRequestsCmd = &cobra.Command{
	Use:   "list-requests UUID",
	Short: "List traffic linked to a test case oldest-first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runListRequestsCommand(cmd, "test-case", args[0])
	},
}

var testCaseCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a test case",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if testCaseCreateTitle == "" {
			return errors.New("test-case create requires a non-empty --title")
		}
		request := testCaseRequest{Title: &testCaseCreateTitle}
		if cmd.Flags().Changed("description") {
			request.Description = &testCaseCreateDescription
		}
		if cmd.Flags().Changed("category") {
			request.Category = &testCaseCreateCategory
		}
		if cmd.Flags().Changed("tag") {
			request.Tags = &testCaseCreateTags
		}
		if cmd.Flags().Changed("note") {
			request.Note = &testCaseCreateNote
		}
		return runTestCaseCommand(cmd, http.MethodPost, "/test-case", "creating test case", "create", request)
	},
}

var testCaseListCmd = &cobra.Command{
	Use:   "list",
	Short: "List test cases newest-first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runTestCaseCommand(cmd, http.MethodGet, "/test-case", "listing test cases", "list", testCaseRequest{})
	},
}

var testCaseGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get one test case",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/test-case/", args[0], "")
		if err != nil {
			return err
		}
		return runTestCaseCommand(cmd, http.MethodGet, path, "getting test case", "get", testCaseRequest{})
	},
}

var testCaseChecklistCmd = &cobra.Command{
	Use:   "checklist",
	Short: "List predefined test cases",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runTestCaseCommand(cmd, http.MethodGet, "/test-case/checklist", "listing test case checklist", "checklist", testCaseRequest{})
	},
}

var testCaseUpdateCmd = &cobra.Command{
	Use:   "update UUID",
	Short: "Update a test case",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var request testCaseRequest
		if cmd.Flags().Changed("title") {
			if testCaseUpdateTitle == "" {
				return errors.New("test-case update requires a non-empty --title")
			}
			request.Title = &testCaseUpdateTitle
		}
		if cmd.Flags().Changed("description") {
			request.Description = &testCaseUpdateDescription
		}
		if cmd.Flags().Changed("category") {
			request.Category = &testCaseUpdateCategory
		}
		if cmd.Flags().Changed("tag") {
			request.Tags = &testCaseUpdateTags
		}
		if cmd.Flags().Changed("note") {
			request.Note = &testCaseUpdateNote
		}
		if request.Title == nil && request.Description == nil && request.Category == nil && request.Tags == nil && request.Note == nil {
			return errors.New("test-case update requires at least one changed flag")
		}
		path, err := serviceIDPath("/test-case/", args[0], "")
		if err != nil {
			return err
		}
		return runTestCaseCommand(cmd, http.MethodPost, path, "updating test case", "update", request)
	},
}

var testCaseDeleteCmd = &cobra.Command{
	Use:   "delete UUID",
	Short: "Delete a test case",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := serviceIDPath("/test-case/", args[0], "")
		if err != nil {
			return err
		}
		return runTestCaseCommand(cmd, http.MethodDelete, path, "deleting test case", "delete", testCaseRequest{})
	},
}

var testCaseLinkCmd = &cobra.Command{
	Use:   "link UUID",
	Short: "Link traffic to a test case",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficMembershipCommand(cmd, "test-case", args[0], testCaseLinkRequest, true)
	},
}

var testCaseUnlinkCmd = &cobra.Command{
	Use:   "unlink UUID",
	Short: "Unlink traffic from a test case",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficMembershipCommand(cmd, "test-case", args[0], testCaseUnlinkRequest, false)
	},
}

func runTrafficMembershipCommand(cmd *cobra.Command, resource, parentID, requestID string, link bool) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	response, err := controlTrafficMembership(ctx, jsonOutput, resource, parentID, requestID, link)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	action := "unlinked"
	preposition := "from"
	label := resource
	if link {
		action = "linked"
		preposition = "to"
	}
	if resource == "test-case" {
		label = "test case"
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "request %s %s %s %s %s successfully\n", requestID, action, preposition, label, parentID)
	return err
}

func controlTrafficMembership(ctx context.Context, asJSON bool, resource, parentID, requestID string, link bool) ([]byte, error) {
	parent, err := parseServiceID(parentID)
	if err != nil {
		return nil, err
	}
	method := http.MethodDelete
	path := "/" + resource + "/" + parent + "/traffic/"
	operation := "unlinking request"
	var body io.Reader
	if link {
		method = http.MethodPost
		path = "/" + resource + "/" + parent + "/traffic"
		operation = "linking request"
		encoded, err := json.Marshal(struct {
			ID string `json:"id"`
		}{ID: requestID})
		if err != nil {
			return nil, fmt.Errorf("encoding traffic link: %w", err)
		}
		body = bytes.NewReader(encoded)
	} else {
		request, err := parseServiceID(requestID)
		if err != nil {
			return nil, err
		}
		path += request
	}
	response, err := callInstance(ctx, method, path, "application/json", body)
	if err != nil {
		return nil, err
	}
	if err := rejectInstanceStatus(operation, response, asJSON); err != nil {
		return nil, err
	}
	return response.Body, nil
}

func runTestCaseCommand(cmd *cobra.Command, method, path, operation, action string, payload testCaseRequest) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var requestBody io.Reader
	if method == http.MethodPost {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding test case request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	called, err := callInstance(ctx, method, path, "application/json", requestBody)
	if err != nil {
		return err
	}
	if err := rejectInstanceStatus(operation, called, jsonOutput); err != nil {
		return err
	}
	response := called.Body
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	if action == "list" {
		return writeTestCaseListHuman(response, cmd.OutOrStdout())
	}
	if action == "checklist" {
		return writeTestCaseChecklistHuman(response, cmd.OutOrStdout())
	}
	if action == "get" {
		return writeTestCaseGetHuman(response, cmd.OutOrStdout())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.ID == "" {
		return errors.New("decoding test case response")
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "test case %s %sd successfully\n", result.ID, action)
	return err
}

func writeTestCaseListHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Category  string   `json:"category"`
			Tags      []string `json:"tags"`
			CreatedAt string   `json:"created_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding test case list: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", item.ID, truncateDisplay(item.Title, trafficPathDisplayLimit), truncateDisplay(item.Category, trafficPathDisplayLimit), truncateDisplay(strings.Join(item.Tags, ","), trafficPathDisplayLimit), item.CreatedAt)
	}
	return writer.Flush()
}

func writeTestCaseGetHuman(body []byte, stdout io.Writer) error {
	var response struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Category    string   `json:"category"`
		Tags        []string `json:"tags"`
		Note        string   `json:"note"`
		CreatedAt   string   `json:"created_at"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding test case: %w", err)
	}
	_, err := fmt.Fprintf(stdout, "id: %s\ntitle: %s\ndescription: %s\ncategory: %s\ntags: %s\nnote: %s\ncreated_at: %s\n", response.ID, response.Title, response.Description, response.Category, strings.Join(response.Tags, ","), response.Note, response.CreatedAt)
	return err
}

func writeTestCaseChecklistHuman(body []byte, stdout io.Writer) error {
	var response struct {
		Items []struct {
			Title    string `json:"title"`
			Category string `json:"category"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("decoding test case checklist: %w", err)
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, item := range response.Items {
		fmt.Fprintf(writer, "%s\t%s\n", truncateDisplay(item.Title, trafficPathDisplayLimit), truncateDisplay(item.Category, trafficPathDisplayLimit))
	}
	return writer.Flush()
}
