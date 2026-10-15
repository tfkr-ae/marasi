package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const trafficSelectionHelp = `Select the pairs with exactly one of:

  --id UUID (repeatable) or --ids-file PATH (one UUID per line, - for
  stdin), which selects those pairs; or

  -q/--query TEXT with --as-of UUID, which selects every pair that
  matches the query and has an id at or below --as-of. Pass the as_of
  of the traffic list page you looked at, so pairs captured since are
  left out. An empty query matches every pair. --exclude hides a
  content type, as in traffic list, and is repeatable.

traffic list --help lists the query fields.`

// trafficSelectionFlags holds one bulk command's selection flags.
type trafficSelectionFlags struct {
	ids     []string
	idsFile string
	query   string
	exclude []string
	asOf    string
}

// addTrafficSelectionFlags registers the selection flags on cmd.
func addTrafficSelectionFlags(cmd *cobra.Command) *trafficSelectionFlags {
	flags := &trafficSelectionFlags{}
	cmd.Flags().StringArrayVar(&flags.ids, "id", nil, "UUID of a pair to select; repeat for more")
	cmd.Flags().StringVar(&flags.idsFile, "ids-file", "", "File of pair UUIDs, one per line, or - for stdin")
	cmd.Flags().StringVarP(&flags.query, "query", "q", "", "AIP-160 query that selects pairs; needs --as-of")
	cmd.Flags().StringArrayVar(&flags.exclude, "exclude", nil, "Content type the query hides, exact or major/*; repeat for more")
	cmd.Flags().StringVar(&flags.asOf, "as-of", "", "Newest pair UUID the query selects, from a traffic list page's as_of")
	return flags
}

// selection checks the selection flags and returns the selection JSON the
// bulk routes take.
func (flags *trafficSelectionFlags) selection(cmd *cobra.Command) (json.RawMessage, error) {
	changed := cmd.Flags().Changed
	byID := changed("id") || changed("ids-file")
	byQuery := changed("query")
	switch {
	case byID && byQuery:
		return nil, errors.New("select pairs with --id or --ids-file, or with --query, not both")
	case changed("id") && changed("ids-file"):
		return nil, errors.New("select pairs with --id or --ids-file, not both")
	case byID && (changed("exclude") || changed("as-of")):
		return nil, errors.New("--exclude and --as-of go with --query")
	case byID:
		ids, err := flags.readIDs(cmd)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			IDs []string `json:"ids"`
		}{IDs: ids})
	case byQuery:
		if !changed("as-of") {
			return nil, errors.New("--query needs --as-of")
		}
		asOf, err := parseServiceID(flags.asOf)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Query     string   `json:"query"`
			Exclusion []string `json:"exclusion,omitempty"`
			AsOf      string   `json:"as_of"`
		}{Query: flags.query, Exclusion: flags.exclude, AsOf: asOf})
	default:
		return nil, errors.New("select pairs with --id, --ids-file, or --query with --as-of")
	}
}

func (flags *trafficSelectionFlags) readIDs(cmd *cobra.Command) ([]string, error) {
	raw := flags.ids
	if cmd.Flags().Changed("ids-file") {
		var reader io.Reader
		if flags.idsFile == "-" {
			reader = cmd.InOrStdin()
		} else {
			file, err := os.Open(flags.idsFile)
			if err != nil {
				return nil, fmt.Errorf("reading ids file: %w", err)
			}
			defer file.Close()
			reader = file
		}
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if line := strings.TrimSpace(scanner.Text()); line != "" {
				raw = append(raw, line)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading ids file: %w", err)
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("the selection has no ids")
	}
	ids := make([]string, len(raw))
	for i, value := range raw {
		id, err := parseServiceID(value)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

var trafficBulkCmd = &cobra.Command{
	Use:   "bulk",
	Short: "Change or delete many request/response pairs at once",
	Long:  "Change or delete many request/response pairs at once.\n\n" + trafficSelectionHelp,
}

var trafficBulkHighlightSelection *trafficSelectionFlags

var trafficBulkHighlightCmd = &cobra.Command{
	Use:   "highlight 1-8|none",
	Short: "Set or clear the highlight of the selected pairs",
	Long:  "Set the highlight of the selected pairs to a number from 1 to 8, or clear it with none.\n\n" + trafficSelectionHelp,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		highlight := json.RawMessage("null")
		verb := "unhighlighted"
		if args[0] != "none" {
			value, err := strconv.Atoi(args[0])
			if err != nil || value < 1 || value > 8 {
				return fmt.Errorf("invalid highlight %q: want 1 to 8 or none", args[0])
			}
			highlight = json.RawMessage(strconv.Itoa(value))
			verb = "highlighted"
		}
		return runTrafficBulk(cmd, trafficBulkHighlightSelection, "highlight", "highlighting traffic", verb, trafficBulkRequest{Highlight: highlight})
	},
}

// trafficBulkLinkFlags holds the target flags of bulk link and unlink.
type trafficBulkLinkFlags struct {
	launchpad, finding, testCase string
}

func addTrafficBulkLinkFlags(cmd *cobra.Command) *trafficBulkLinkFlags {
	flags := &trafficBulkLinkFlags{}
	cmd.Flags().StringVar(&flags.launchpad, "launchpad", "", "UUID of the target launchpad")
	cmd.Flags().StringVar(&flags.finding, "finding", "", "UUID of the target finding")
	cmd.Flags().StringVar(&flags.testCase, "test-case", "", "UUID of the target test case")
	return flags
}

// request returns a bulk request naming the one target the flags set.
func (flags *trafficBulkLinkFlags) request(cmd *cobra.Command) (trafficBulkRequest, error) {
	var request trafficBulkRequest
	set := 0
	for name, target := range map[string]struct {
		raw   string
		field *string
	}{
		"launchpad": {flags.launchpad, &request.LaunchpadID},
		"finding":   {flags.finding, &request.FindingID},
		"test-case": {flags.testCase, &request.TestCaseID},
	} {
		if !cmd.Flags().Changed(name) {
			continue
		}
		set++
		id, err := parseServiceID(target.raw)
		if err != nil {
			return trafficBulkRequest{}, err
		}
		*target.field = id
	}
	if set != 1 {
		return trafficBulkRequest{}, errors.New("name exactly one of --launchpad, --finding, or --test-case")
	}
	return request, nil
}

func newTrafficBulkLinkCmd(route, short, operation, verb string) *cobra.Command {
	var selection *trafficSelectionFlags
	var targets *trafficBulkLinkFlags
	cmd := &cobra.Command{
		Use:   route + " --launchpad|--finding|--test-case UUID",
		Short: short,
		Long:  short + ". Name exactly one target.\n\n" + trafficSelectionHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request, err := targets.request(cmd)
			if err != nil {
				return err
			}
			return runTrafficBulk(cmd, selection, route, operation, verb, request)
		},
	}
	selection = addTrafficSelectionFlags(cmd)
	targets = addTrafficBulkLinkFlags(cmd)
	return cmd
}

var trafficBulkMetadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "Set or clear one metadata key on the selected pairs",
}

var trafficBulkMetadataSetSelection *trafficSelectionFlags

var trafficBulkMetadataSetCmd = &cobra.Command{
	Use:   "set KEY JSON",
	Short: "Set one metadata key on the selected pairs",
	Long: `Set one metadata key on the selected pairs to a JSON value. KEY is a
dotted path such as triage.state; the value replaces whatever is at that
path and leaves the rest of the metadata alone. Quote strings as JSON, for
example '"seen"'.

` + trafficSelectionHelp,
	Example: `  marasi traffic bulk metadata set triage.state '"seen"' --id "$ID"
  marasi traffic bulk metadata set score 7 -q 'host = "api.example.com"' --as-of "$AS_OF"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !json.Valid([]byte(args[1])) {
			return fmt.Errorf("invalid JSON value %q", args[1])
		}
		key := args[0]
		return runTrafficBulk(cmd, trafficBulkMetadataSetSelection, "metadata", "setting metadata", "updated", trafficBulkRequest{Key: &key, Value: json.RawMessage(args[1])})
	},
}

var trafficBulkMetadataClearSelection *trafficSelectionFlags

var trafficBulkMetadataClearCmd = &cobra.Command{
	Use:   "clear KEY",
	Short: "Remove one metadata key from the selected pairs",
	Long:  "Remove one metadata key, a dotted path such as triage.state, from the selected pairs.\n\n" + trafficSelectionHelp,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		return runTrafficBulk(cmd, trafficBulkMetadataClearSelection, "metadata", "clearing metadata", "updated", trafficBulkRequest{Key: &key, Clear: true})
	},
}

var trafficBulkNoteCmd = &cobra.Command{
	Use:   "note",
	Short: "Append to or clear the notes of the selected pairs",
}

var trafficBulkNoteAppendSelection *trafficSelectionFlags
var trafficBulkNoteAppendFile string

var trafficBulkNoteAppendCmd = &cobra.Command{
	Use:   "append [TEXT]",
	Short: "Append text to the notes of the selected pairs",
	Long: `Append text to the notes of the selected pairs. A pair with no note gets
the text as its note; a pair with a note gets a blank line and then the
text. Pass exactly one of TEXT, --file, or piped stdin. With --ids-file -
stdin holds the ids, so pass TEXT or --file.

` + trafficSelectionHelp,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		input := textInput{what: "note", file: trafficBulkNoteAppendFile}
		if trafficBulkNoteAppendSelection.idsFile == "-" {
			input.stdinTaken = "--ids-file is -"
		}
		text, err := input.read(cmd, args)
		if err != nil {
			return err
		}
		return runTrafficBulk(cmd, trafficBulkNoteAppendSelection, "note", "appending notes", "noted", trafficBulkRequest{Append: &text})
	},
}

var trafficBulkNoteClearSelection *trafficSelectionFlags

var trafficBulkNoteClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the notes of the selected pairs",
	Long:  "Clear the notes of the selected pairs. It does not ask first. The count is the pairs that had a note.\n\n" + trafficSelectionHelp,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficBulk(cmd, trafficBulkNoteClearSelection, "note", "clearing notes", "cleared", trafficBulkRequest{Clear: true})
	},
}

var trafficBulkDeleteSelection *trafficSelectionFlags

var trafficBulkDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete the selected pairs and everything they own",
	Long: `Delete the selected pairs and everything that belongs to them: notes,
highlights, links to launchpads, findings, test cases and Armory runs,
WebSocket connections and messages, and proxy logs. Pairs that are evidence
for a finding are deleted too. It does not ask first.

` + trafficSelectionHelp,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTrafficBulk(cmd, trafficBulkDeleteSelection, "delete", "deleting traffic", "deleted", trafficBulkRequest{})
	},
}

func init() {
	trafficBulkHighlightSelection = addTrafficSelectionFlags(trafficBulkHighlightCmd)
	trafficBulkMetadataSetSelection = addTrafficSelectionFlags(trafficBulkMetadataSetCmd)
	trafficBulkMetadataClearSelection = addTrafficSelectionFlags(trafficBulkMetadataClearCmd)
	trafficBulkNoteAppendSelection = addTrafficSelectionFlags(trafficBulkNoteAppendCmd)
	trafficBulkNoteAppendCmd.Flags().StringVar(&trafficBulkNoteAppendFile, "file", "", "Read the text to append from a file")
	trafficBulkNoteClearSelection = addTrafficSelectionFlags(trafficBulkNoteClearCmd)
	trafficBulkDeleteSelection = addTrafficSelectionFlags(trafficBulkDeleteCmd)
	trafficBulkMetadataCmd.AddCommand(trafficBulkMetadataSetCmd, trafficBulkMetadataClearCmd)
	trafficBulkNoteCmd.AddCommand(trafficBulkNoteAppendCmd, trafficBulkNoteClearCmd)
	trafficBulkCmd.AddCommand(
		trafficBulkHighlightCmd,
		newTrafficBulkLinkCmd("link", "Link the selected pairs to a launchpad, finding, or test case", "linking traffic", "linked"),
		newTrafficBulkLinkCmd("unlink", "Unlink the selected pairs from a launchpad, finding, or test case", "unlinking traffic", "unlinked"),
		trafficBulkMetadataCmd,
		trafficBulkNoteCmd,
		trafficBulkDeleteCmd,
	)
	trafficCmd.AddCommand(trafficBulkCmd)
}

// trafficBulkRequest is a bulk route body: the selection and the fields of
// one operation.
type trafficBulkRequest struct {
	Selection   json.RawMessage `json:"selection"`
	Highlight   json.RawMessage `json:"highlight,omitempty"`
	LaunchpadID string          `json:"launchpad_id,omitempty"`
	FindingID   string          `json:"finding_id,omitempty"`
	TestCaseID  string          `json:"test_case_id,omitempty"`
	Key         *string         `json:"key,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
	Append      *string         `json:"append,omitempty"`
	Clear       bool            `json:"clear,omitempty"`
}

// runTrafficBulk posts one bulk operation and reports its count in human
// mode. request carries the operation's fields; its selection is set here.
func runTrafficBulk(cmd *cobra.Command, flags *trafficSelectionFlags, route, operation, verb string, request trafficBulkRequest) error {
	selection, err := flags.selection(cmd)
	if err != nil {
		return err
	}
	request.Selection = selection
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encoding bulk request: %w", err)
	}
	body, err := runControlRequest(cmd, http.MethodPost, "/traffic/bulk/"+route, operation, payload)
	if err != nil {
		return err
	}
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decoding bulk result: %w", err)
	}
	noun := "pairs"
	if result.Count == 1 {
		noun = "pair"
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(), "%d %s %s\n", result.Count, noun, verb)
	return err
}
