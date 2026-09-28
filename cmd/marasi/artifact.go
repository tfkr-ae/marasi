package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

var artifactUploadTestCase string
var artifactUploadFinding string
var artifactUploadFile string
var artifactUploadMIME string
var artifactDownloadOutput string

type artifactMetadata struct {
	ID         string  `json:"id"`
	Filename   string  `json:"filename"`
	MIMEType   string  `json:"mime_type"`
	Size       int64   `json:"size"`
	TestCaseID *string `json:"test_case_id"`
	FindingID  *string `json:"finding_id"`
	CreatedAt  string  `json:"created_at"`
}

func init() {
	artifactUploadCmd.Flags().StringVar(&artifactUploadTestCase, "test-case", "", "Test case UUID. Set this or --finding, not both")
	artifactUploadCmd.Flags().StringVar(&artifactUploadFinding, "finding", "", "Finding UUID. Set this or --test-case, not both")
	artifactUploadCmd.Flags().StringVar(&artifactUploadFile, "file", "", "File to upload")
	artifactUploadCmd.Flags().StringVar(&artifactUploadMIME, "mime", "", "MIME type. Defaults from the file extension")
	artifactUploadCmd.MarkFlagRequired("file")
	artifactDownloadCmd.Flags().StringVar(&artifactDownloadOutput, "output", "", "File to write. Defaults to the artifact file name in the current directory")
	artifactCmd.AddCommand(artifactUploadCmd, artifactGetCmd, artifactDownloadCmd, artifactDeleteCmd)
	rootCmd.AddCommand(artifactCmd)
}

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Upload and download files for findings and test cases",
}

var artifactUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload a file for a test case or a finding",
	Long:  "Upload a file. Set exactly one of --test-case or --finding.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if (artifactUploadTestCase == "") == (artifactUploadFinding == "") {
			return errors.New("artifact upload requires exactly one of --test-case or --finding")
		}
		if artifactUploadFile == "" {
			return errors.New("artifact upload requires --file")
		}
		parent, rawParentID := "test-case", artifactUploadTestCase
		if artifactUploadFinding != "" {
			parent, rawParentID = "finding", artifactUploadFinding
		}
		parentID, err := parseServiceID(rawParentID)
		if err != nil {
			return err
		}
		mimeType := artifactUploadMIME
		if mimeType == "" {
			mimeType = mime.TypeByExtension(filepath.Ext(artifactUploadFile))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
		}
		return runArtifactUpload(cmd, parent, parentID, artifactUploadFile, mimeType)
	},
}

var artifactGetCmd = &cobra.Command{
	Use:   "get UUID",
	Short: "Get artifact metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArtifactGet(cmd, args[0])
	},
}

var artifactDownloadCmd = &cobra.Command{
	Use:   "download UUID",
	Short: "Download an artifact",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if jsonOutput {
			return errors.New("artifact download does not support --json")
		}
		return runArtifactDownload(cmd, args[0], artifactDownloadOutput)
	},
}

var artifactDeleteCmd = &cobra.Command{
	Use:   "delete UUID",
	Short: "Delete an artifact",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArtifactDelete(cmd, args[0])
	},
}

func runArtifactUpload(cmd *cobra.Command, parent, parentID, path, mimeType string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening artifact file: %w", err)
	}
	defer file.Close()
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	endpoint := "/" + parent + "/" + parentID + "/artifact?" + url.Values{"filename": {filepath.Base(path)}}.Encode()
	response, err := controlArtifactRequest(ctx, jsonOutput, http.MethodPost, endpoint, mimeType, file, "uploading artifact")
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	metadata, err := decodeArtifactMetadata(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "artifact %s uploaded successfully\n", metadata.ID)
	return err
}

func runArtifactGet(cmd *cobra.Command, id string) error {
	id, err := parseServiceID(id)
	if err != nil {
		return err
	}
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	response, err := controlArtifactRequest(ctx, jsonOutput, http.MethodGet, "/artifact/"+id, "", nil, "getting artifact")
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	metadata, err := decodeArtifactMetadata(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "id: %s\nfilename: %s\nmime_type: %s\nsize: %d\ntest_case_id: %s\nfinding_id: %s\ncreated_at: %s\n", metadata.ID, metadata.Filename, metadata.MIMEType, metadata.Size, optionalString(metadata.TestCaseID), optionalString(metadata.FindingID), metadata.CreatedAt)
	return err
}

func runArtifactDownload(cmd *cobra.Command, id, output string) error {
	id, err := parseServiceID(id)
	if err != nil {
		return err
	}
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	metadataBody, err := controlArtifactRequest(ctx, false, http.MethodGet, "/artifact/"+id, "", nil, "getting artifact")
	if err != nil {
		return err
	}
	metadata, err := decodeArtifactMetadata(metadataBody)
	if err != nil {
		return err
	}
	if output == "" {
		output = filepath.Join(".", filepath.Base(metadata.Filename))
	}
	contents, err := controlArtifactRequest(ctx, false, http.MethodGet, "/artifact/"+id+"/content", "", nil, "downloading artifact")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, contents, 0600); err != nil {
		return fmt.Errorf("writing artifact: %w", err)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), output)
	return err
}

func runArtifactDelete(cmd *cobra.Command, id string) error {
	id, err := parseServiceID(id)
	if err != nil {
		return err
	}
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	response, err := controlArtifactRequest(ctx, jsonOutput, http.MethodDelete, "/artifact/"+id, "", nil, "deleting artifact")
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(response)
		return err
	}
	metadata, err := decodeArtifactMetadata(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "artifact %s deleted successfully\n", metadata.ID)
	return err
}

func artifactCommandContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
}

func controlArtifactRequest(ctx context.Context, asJSON bool, method, path, contentType string, body io.Reader, operation string) ([]byte, error) {
	response, err := callInstance(ctx, method, path, contentType, body)
	if err != nil {
		return nil, err
	}
	if err := rejectInstanceStatus(operation, response, asJSON); err != nil {
		return nil, err
	}
	return response.Body, nil
}

func decodeArtifactMetadata(body []byte) (artifactMetadata, error) {
	var metadata artifactMetadata
	if err := json.Unmarshal(body, &metadata); err != nil || metadata.ID == "" {
		return artifactMetadata{}, errors.New("decoding artifact response")
	}
	return metadata, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
