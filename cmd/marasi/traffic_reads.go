package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

const (
	sitemapBuildingNotice = "notice: the sitemap is still building; counts may be incomplete"
	indexBuildingNotice   = "notice: the traffic index is still building; text results may be incomplete"
)

var (
	trafficSitemapNode    string
	trafficSitemapQuery   string
	trafficSitemapExclude []string
	trafficSitemapAsOf    string
	trafficSitemapLimit   string
	trafficSitemapCursor  string

	trafficCountQuery   string
	trafficCountExclude []string
	trafficCountAsOf    string
	trafficCountAfter   string

	trafficValidateQuery   string
	trafficValidateExclude []string
	trafficValidateOrderBy string
)

func init() {
	flags := trafficSitemapCmd.Flags()
	flags.StringVar(&trafficSitemapNode, "node", "", "Name of the node whose children to list; omit for the origins")
	flags.StringVarP(&trafficSitemapQuery, "query", "q", "", "AIP-160 query; counts only matching pairs and needs --as-of")
	flags.StringArrayVar(&trafficSitemapExclude, "exclude", nil, "Content type to leave out, exact or major/*; repeat for more")
	flags.StringVar(&trafficSitemapAsOf, "as-of", "", "Newest pair UUID a --query counts, from a traffic list page's as_of")
	flags.StringVar(&trafficSitemapLimit, "limit", "100", "Maximum number of children to return, from 1 to 500")
	flags.StringVar(&trafficSitemapCursor, "cursor", "", "next_cursor from an earlier page")

	flags = trafficCountCmd.Flags()
	flags.StringVarP(&trafficCountQuery, "query", "q", "", "AIP-160 query that narrows the count; see traffic list --help")
	flags.StringArrayVar(&trafficCountExclude, "exclude", nil, "Content type to hide, exact or major/*; repeat for more")
	flags.StringVar(&trafficCountAsOf, "as-of", "", "Count only pairs with an id at or below this UUID")
	flags.StringVar(&trafficCountAfter, "after", "", "Count only pairs with an id above this UUID, such as a page's as_of")

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

Paging: stderr shows next_cursor; pass it back with --cursor.`,
	Example: `  marasi traffic sitemap
  marasi traffic sitemap --node 'https://api.example.com/users/{int}'
  marasi traffic sitemap -q 'status_code >= 500' --as-of "$AS_OF"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		limit, err := parsePageLimit(trafficSitemapLimit)
		if err != nil {
			return err
		}
		query := url.Values{}
		query.Set("limit", limit)
		if cmd.Flags().Changed("node") {
			query.Set("node", trafficSitemapNode)
		}
		if trafficSitemapCursor != "" {
			query.Set("cursor", trafficSitemapCursor)
		}
		for _, value := range trafficSitemapExclude {
			query.Add("exclude", value)
		}
		switch hasQuery, hasAsOf := cmd.Flags().Changed("query"), cmd.Flags().Changed("as-of"); {
		case hasQuery && !hasAsOf:
			return errors.New("--query needs --as-of")
		case hasAsOf && !hasQuery:
			return errors.New("--as-of goes with --query")
		case hasQuery:
			asOf, err := parseServiceID(trafficSitemapAsOf)
			if err != nil {
				return err
			}
			query.Set("q", trafficSitemapQuery)
			query.Set("as_of", asOf)
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/traffic/sitemap?"+query.Encode(), "listing sitemap", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
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
		if trafficCountQuery != "" {
			query.Set("q", trafficCountQuery)
		}
		for _, value := range trafficCountExclude {
			query.Add("exclude", value)
		}
		for name, raw := range map[string]string{"as_of": trafficCountAsOf, "after": trafficCountAfter} {
			if raw == "" {
				continue
			}
			id, err := parseServiceID(raw)
			if err != nil {
				return err
			}
			query.Set(name, id)
		}
		body, err := runControlRequest(cmd, http.MethodGet, "/traffic/count?"+query.Encode(), "counting traffic", nil)
		if err != nil {
			return err
		}
		if jsonOutput {
			_, err = cmd.OutOrStdout().Write(body)
			return err
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
			_, err = cmd.OutOrStdout().Write(body)
			return err
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
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		for _, item := range counts.Items {
			contentType := item.ContentType
			if contentType == "" {
				contentType = "-"
			}
			fmt.Fprintf(writer, "%s\t%d\n", truncateDisplay(contentType, 80), item.Count)
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
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
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
