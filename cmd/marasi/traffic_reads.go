package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

const (
	// contentTypeDisplayLimit is the widest content type traffic types prints.
	contentTypeDisplayLimit = 80

	sitemapBuildingNotice = "notice: the sitemap is still building; counts may be incomplete"
	indexBuildingNotice   = "notice: the traffic index is still building; text results may be incomplete"
)

var (
	trafficSitemapNodes  []string
	trafficSitemapScope  string
	trafficSitemapFilter *trafficQueryFlags
	trafficSitemapLimit  string
	trafficSitemapCursor string

	trafficCountFilter *trafficQueryFlags
	trafficCountAfter  string

	trafficValidateQuery   string
	trafficValidateExclude []string
	trafficValidateOrderBy string
)

func init() {
	flags := trafficSitemapCmd.Flags()
	flags.StringArrayVar(&trafficSitemapNodes, "node", nil, "Name of the node whose children to list; omit for the origins, repeat to list several levels")
	flags.StringVar(&trafficSitemapScope, "scope", "", "Levels read: all lists every child, matching only the children with -q matches; all by default, matching with -q")
	flags.StringVar(&trafficSitemapLimit, "limit", "100", "Maximum number of children to return, from 1 to 500")
	flags.StringVar(&trafficSitemapCursor, "cursor", "", "next_cursor from an earlier page")

	trafficSitemapFilter = addTrafficQueryFlags(trafficSitemapCmd, true)

	trafficCountFilter = addTrafficQueryFlags(trafficCountCmd, false)
	trafficCountCmd.Flags().StringVar(&trafficCountAfter, "after", "", "Count only pairs with an id above this UUID, such as a page's as_of")

	flags = trafficValidateCmd.Flags()
	flags.StringVarP(&trafficValidateQuery, "query", "q", "", "AIP-160 query to check; see traffic list --help")
	flags.StringArrayVar(&trafficValidateExclude, "exclude", nil, "Content type exclusion to check; repeat for more")
	flags.StringVar(&trafficValidateOrderBy, "order-by", "", "Sort to check, as traffic list --order-by takes it")

	trafficCmd.AddCommand(trafficSitemapCmd, trafficCountCmd, trafficTypesCmd, trafficValidateCmd)
}

var trafficSitemapCmd = &cobra.Command{
	Use:   "sitemap",
	Short: "List the children of a sitemap node, most pairs first",
	Long: `List the children of a sitemap node, most pairs first. Without --node it
lists the origins, such as https://api.example.com. Pass a printed name
back with --node to list that node's children.

Each row is the name, the pairs in its subtree, the pairs among them with a
5xx response, and + when the node has children.

-q counts only the pairs that match a query, as in traffic list, and needs
--as-of, the as_of of the traffic list page you looked at. --exclude leaves
content types out of the counts.

Paging: stderr shows next_cursor; pass it back with --cursor.

Repeat --node, or pass --scope, to read several levels in one call. Each
level prints under a "# <node>" line, and each row adds the matching pairs
and 5xx responses, or - without -q. With -q, --scope matching (the default)
lists only the children with matches, and --scope all lists every child,
counted over all traffic, with its matching counts. --exclude applies to
both counts. A node that does not exist prints as (missing).`,
	Example: `  marasi traffic sitemap
  marasi traffic sitemap --node 'https://api.example.com/users/{int}'
  marasi traffic sitemap -q 'status_code >= 500' --as-of "$AS_OF"
  marasi traffic sitemap --node '' --node https://api.example.com --scope all -q 'status_code >= 500' --as-of "$AS_OF"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, err := parsePageLimit(trafficSitemapLimit)
		if err != nil {
			return err
		}
		if len(trafficSitemapNodes) > 1 || cmd.Flags().Changed("scope") {
			return runSitemapLevels(cmd, limit)
		}
		query := url.Values{}
		query.Set("limit", limit)
		if len(trafficSitemapNodes) == 1 {
			query.Set("node", trafficSitemapNodes[0])
		}
		if trafficSitemapCursor != "" {
			query.Set("cursor", trafficSitemapCursor)
		}
		if err := trafficSitemapFilter.addTo(cmd, query); err != nil {
			return err
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/traffic/sitemap?"+query.Encode(), "listing sitemap", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		return writeSitemapHuman(body, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var trafficCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Count the pairs that match a query",
	Long: `Count the pairs that match a query, as traffic list takes it. A count
past the service's cap prints with a trailing +.

--as-of counts only pairs at or below an id; --after counts only pairs
above one. Pass a traffic list page's as_of with --after to count the
matches captured since that page.`,
	Example: `  marasi traffic count -q 'status_code >= 500'
  marasi traffic count --after "$AS_OF"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		query := url.Values{}
		if err := trafficCountFilter.addTo(cmd, query); err != nil {
			return err
		}
		if cmd.Flags().Changed("after") {
			if trafficCountAfter == "" {
				return errors.New("--after is empty; pass a pair UUID")
			}
			after, err := parseServiceID(trafficCountAfter)
			if err != nil {
				return err
			}
			query.Set("after", after)
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/traffic/count?"+query.Encode(), "counting traffic", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var count struct {
			Count  int  `json:"count"`
			Capped bool `json:"capped"`
			Index  struct {
				Complete bool `json:"complete"`
			} `json:"index"`
			Sitemap *struct {
				Complete bool `json:"complete"`
			} `json:"sitemap"`
		}
		if err := json.Unmarshal(body, &count); err != nil {
			return fmt.Errorf("decoding traffic count: %w", err)
		}
		suffix := ""
		if count.Capped {
			suffix = "+"
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%d%s\n", count.Count, suffix); err != nil {
			return err
		}
		if count.Sitemap != nil && !count.Sitemap.Complete {
			fmt.Fprintln(cmd.ErrOrStderr(), sitemapBuildingNotice)
		}
		if !count.Index.Complete {
			fmt.Fprintln(cmd.ErrOrStderr(), indexBuildingNotice)
		}
		return nil
	},
}

var trafficTypesCmd = &cobra.Command{
	Use:   "types",
	Short: "Count the pairs of each response content type",
	Long: `Count the pairs of each response content type, most first. A pair with
no content type yet counts under -. Use a type with traffic list --exclude.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := runControlRequest(cmd, http.MethodGet, "/traffic/content-types", "counting content types", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSONBody(cmd, body)
		}
		var counts struct {
			Items []struct {
				ContentType string `json:"content_type"`
				Count       int    `json:"count"`
			} `json:"items"`
			Sitemap struct {
				Complete bool `json:"complete"`
			} `json:"sitemap"`
		}
		if err := json.Unmarshal(body, &counts); err != nil {
			return fmt.Errorf("decoding content types: %w", err)
		}
		writer := newTableWriter(cmd.OutOrStdout())
		for _, item := range counts.Items {
			contentType := item.ContentType
			if contentType == "" {
				contentType = "-"
			}
			fmt.Fprintf(writer, "%s\t%d\n", truncateDisplay(contentType, contentTypeDisplayLimit), item.Count)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if !counts.Sitemap.Complete {
			fmt.Fprintln(cmd.ErrOrStderr(), sitemapBuildingNotice)
		}
		return nil
	},
}

var trafficValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check a traffic query, exclusion and sort without running them",
	Long: `Check a traffic query, content-type exclusion and sort without running
them. It needs no open project. It exits 0 when they are valid. When one is
not, it prints the error with its position in the query and exits 1.`,
	Example: `  marasi traffic validate -q 'status_code >= 500 AND host = "*.example.com"'
  marasi traffic validate --order-by 'length desc' --exclude 'image/*'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		query := url.Values{}
		if trafficValidateQuery != "" {
			query.Set("q", trafficValidateQuery)
		}
		for _, value := range trafficValidateExclude {
			query.Add("exclude", value)
		}
		if trafficValidateOrderBy != "" {
			query.Set("order_by", trafficValidateOrderBy)
		}
		if _, err := runControlRequestStatus(cmd, http.MethodGet, "/traffic/query?"+query.Encode(), "validating query", nil, http.StatusNoContent); err != nil {
			return err
		}
		if jsonOutput {
			_, err := io.WriteString(cmd.OutOrStdout(), `{"valid":true}`+"\n")
			return err
		}
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), "query is valid")
		return err
	},
}

// sitemapLevelsSelection is the selection of a POST /traffic/sitemap/levels
// body.
type sitemapLevelsSelection struct {
	Query     string   `json:"query"`
	Exclusion []string `json:"exclusion"`
	AsOf      string   `json:"as_of"`
}

// runSitemapLevels reads every --node in one POST /traffic/sitemap/levels
// call.
func runSitemapLevels(cmd *cobra.Command, limit string) error {
	asOf, err := trafficSitemapFilter.asOfID(cmd)
	if err != nil {
		return err
	}
	hasQuery := cmd.Flags().Changed("query")
	scope := trafficSitemapScope
	switch {
	case scope == "" && hasQuery:
		scope = "matching"
	case scope == "":
		scope = "all"
	case scope != "all" && scope != "matching":
		return fmt.Errorf("--scope is %q; use all or matching", scope)
	case scope == "matching" && !hasQuery:
		return errors.New("--scope matching needs -q")
	}
	nodes := trafficSitemapNodes
	if len(nodes) == 0 {
		nodes = []string{""}
	}
	if trafficSitemapCursor != "" && len(nodes) > 1 {
		return errors.New("--cursor continues one level; pass a single --node")
	}
	parsedLimit, _ := strconv.Atoi(limit) // parsePageLimit checked it
	exclusion := append([]string{}, trafficSitemapFilter.exclude...)
	request := struct {
		Nodes     []string                `json:"nodes"`
		Scope     string                  `json:"scope"`
		Exclusion []string                `json:"exclusion"`
		Selection *sitemapLevelsSelection `json:"selection,omitempty"`
		Cursors   map[string]string       `json:"cursors,omitempty"`
		Limit     int                     `json:"limit"`
	}{Nodes: nodes, Scope: scope, Exclusion: exclusion, Limit: parsedLimit}
	if hasQuery {
		request.Selection = &sitemapLevelsSelection{Query: trafficSitemapFilter.query, Exclusion: exclusion, AsOf: asOf}
	}
	if trafficSitemapCursor != "" {
		request.Cursors = map[string]string{nodes[0]: trafficSitemapCursor}
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false) // keep >= in a query readable
	if err := encoder.Encode(request); err != nil {
		return err
	}
	body, err := runControlRequest(cmd, http.MethodPost, "/traffic/sitemap/levels", "listing sitemap levels", bytes.TrimSpace(encoded.Bytes()))
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSONBody(cmd, body)
	}
	return writeSitemapLevelsHuman(body, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func writeSitemapLevelsHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Levels []struct {
			Node  string `json:"node"`
			Items []struct {
				Name           string `json:"name"`
				Pairs          int64  `json:"pairs"`
				Errors         int64  `json:"errors"`
				HasChildren    bool   `json:"has_children"`
				Matching       *int64 `json:"matching"`
				MatchingErrors *int64 `json:"matching_errors"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
			Missing    bool    `json:"missing"`
		} `json:"levels"`
		Sitemap struct {
			Complete bool `json:"complete"`
		} `json:"sitemap"`
		Index *struct {
			Complete bool `json:"complete"`
		} `json:"index"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding sitemap levels: %w", err)
	}
	count := func(value *int64) string {
		if value == nil {
			return "-"
		}
		return strconv.FormatInt(*value, 10)
	}
	for _, level := range page.Levels {
		header := "# " + level.Node
		if level.Node == "" {
			header = "# (origins)"
		}
		if level.Missing {
			header += " (missing)"
		}
		fmt.Fprintln(stdout, header)
		writer := newTableWriter(stdout)
		for _, item := range level.Items {
			children := ""
			if item.HasChildren {
				children = "+"
			}
			fmt.Fprintf(writer, "%s\t%d\t%d\t%s\t%s\t%s\n", item.Name, item.Pairs, item.Errors, count(item.Matching), count(item.MatchingErrors), children)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if level.NextCursor != nil {
			fmt.Fprintf(stderr, "next_cursor=%s node=%s\n", *level.NextCursor, level.Node)
		}
	}
	if !page.Sitemap.Complete {
		fmt.Fprintln(stderr, sitemapBuildingNotice)
	}
	if page.Index != nil && !page.Index.Complete {
		fmt.Fprintln(stderr, indexBuildingNotice)
	}
	return nil
}

func writeSitemapHuman(body []byte, stdout, stderr io.Writer) error {
	var page struct {
		Items []struct {
			Name        string `json:"name"`
			Pairs       int64  `json:"pairs"`
			Errors      int64  `json:"errors"`
			HasChildren bool   `json:"has_children"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
		Sitemap    struct {
			Complete bool `json:"complete"`
		} `json:"sitemap"`
		Index *struct {
			Complete bool `json:"complete"`
		} `json:"index"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return fmt.Errorf("decoding sitemap: %w", err)
	}
	writer := newTableWriter(stdout)
	for _, item := range page.Items {
		children := ""
		if item.HasChildren {
			children = "+"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.Name, strconv.FormatInt(item.Pairs, 10), strconv.FormatInt(item.Errors, 10), children)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if page.NextCursor != nil {
		fmt.Fprintf(stderr, "next_cursor=%s\n", *page.NextCursor)
	}
	if !page.Sitemap.Complete {
		fmt.Fprintln(stderr, sitemapBuildingNotice)
	}
	if page.Index != nil && !page.Index.Complete {
		fmt.Fprintln(stderr, indexBuildingNotice)
	}
	return nil
}
