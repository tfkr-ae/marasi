package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func runListRequestsCommand(cmd *cobra.Command, parent, id string) error {
	path, err := serviceIDPath("/"+parent+"/", id, "")
	if err != nil {
		return err
	}
	body, err := runControlRequest(cmd, http.MethodGet, path, "listing "+parent+" requests", nil)
	if err != nil {
		return err
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decoding linked requests: %w", err)
	}
	if list.Items == nil {
		list.Items = []json.RawMessage{}
	}
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(list)
	}
	return writeTrafficListHuman(body, cmd.OutOrStdout(), cmd.ErrOrStderr())
}
