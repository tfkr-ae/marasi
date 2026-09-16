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
	"github.com/tfkr-ae/marasi/service"
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
	artifactUploadCmd.Flags().StringVar(&artifactUploadTestCase, "test-case", "", "Test case UUID")
	artifactUploadCmd.Flags().StringVar(&artifactUploadFinding, "finding", "", "Finding UUID")
	artifactUploadCmd.Flags().StringVar(&artifactUploadFile, "file", "", "File to upload")
	artifactUploadCmd.Flags().StringVar(&artifactUploadMIME, "mime", "", "Artifact MIME type")
	artifactUploadCmd.MarkFlagRequired("file")
	artifactDownloadCmd.Flags().StringVar(&artifactDownloadOutput, "output", "", "Output path")
	artifactCmd.AddCommand(artifactUploadCmd, artifactGetCmd, artifactDownloadCmd, artifactDeleteCmd)
	rootCmd.AddCommand(artifactCmd)
}

var artifactCmd = &cobra.Command{
	Use:   "artifact",
	Short: "Manage artifacts for a service instance",
}

var artifactUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload an artifact",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if (artifactUploadTestCase == "") == (artifactUploadFinding == "") {
			return errors.New("artifact upload requires exactly one of --test-case or --finding")
		}
		if artifactUploadFile == "" {
			return errors.New("artifact upload requires --file")
		}
		parent, parentID := "test-case", artifactUploadTestCase
		if artifactUploadFinding != "" {
			parent, parentID = "finding", artifactUploadFinding
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
	Use:   "get uuid",
	Short: "Get artifact metadata",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArtifactGet(cmd, args[0])
	},
}

var artifactDownloadCmd = &cobra.Command{
	Use:   "download uuid",
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
	Use:   "delete uuid",
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
	response, err := controlArtifactRequest(ctx, instancePath, instance, jsonOutput, http.MethodPost, endpoint, mimeType, file, "uploading artifact")
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
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	response, err := controlArtifactRequest(ctx, instancePath, instance, jsonOutput, http.MethodGet, "/artifact/"+id, "", nil, "getting artifact")
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
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	metadataBody, err := controlArtifactRequest(ctx, instancePath, instance, false, http.MethodGet, "/artifact/"+id, "", nil, "getting artifact")
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
	contents, err := controlArtifactRequest(ctx, instancePath, instance, false, http.MethodGet, "/artifact/"+id+"/content", "", nil, "downloading artifact")
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
	ctx, cancel := artifactCommandContext(cmd)
	defer cancel()
	response, err := controlArtifactRequest(ctx, instancePath, instance, jsonOutput, http.MethodDelete, "/artifact/"+id, "", nil, "deleting artifact")
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

func controlArtifactRequest(ctx context.Context, instancePath, instanceName string, asJSON bool, method, path, contentType string, body io.Reader, operation string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://marasi"+path, body)
	if err != nil {
		return nil, fmt.Errorf("creating artifact request: %w", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
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
	responseBody, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(wrapError("reading artifact response", readErr), wrapError("closing artifact response", closeErr))
	}
	if response.StatusCode != http.StatusOK {
		if asJSON {
			return nil, controlAPIError(operation, response.Status, responseBody)
		}
		return nil, fmt.Errorf("%s: %s", operation, response.Status)
	}
	return responseBody, nil
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
