package main

import (
	"errors"
	"net/url"

	"github.com/spf13/cobra"
)

// trafficQueryFlags holds the -q/--query, --exclude and --as-of flags that
// traffic sitemap, traffic count and the traffic bulk selections share.
type trafficQueryFlags struct {
	query   string
	exclude []string
	asOf    string
	// asOfPairsQuery is true when --query and --as-of only make sense
	// together, as in sitemap and the bulk selections.
	asOfPairsQuery bool
}

// addTrafficQueryFlags registers the query flags on cmd. With
// asOfPairsQuery, --query needs --as-of and --as-of needs --query.
func addTrafficQueryFlags(cmd *cobra.Command, asOfPairsQuery bool) *trafficQueryFlags {
	flags := &trafficQueryFlags{asOfPairsQuery: asOfPairsQuery}
	queryUsage := "AIP-160 query; see traffic list --help"
	if asOfPairsQuery {
		queryUsage += "; needs --as-of"
	}
	cmd.Flags().StringVarP(&flags.query, "query", "q", "", queryUsage)
	cmd.Flags().StringArrayVar(&flags.exclude, "exclude", nil, "Content type to hide, exact or major/*; repeat for more")
	cmd.Flags().StringVar(&flags.asOf, "as-of", "", "Only pairs with an id at or below this UUID, such as a traffic list page's as_of")
	return flags
}

// asOfID checks how --query and --as-of combine and returns the parsed
// --as-of UUID, or "" when --as-of is not set. A set but empty --as-of is
// rejected.
func (flags *trafficQueryFlags) asOfID(cmd *cobra.Command) (string, error) {
	hasQuery, hasAsOf := cmd.Flags().Changed("query"), cmd.Flags().Changed("as-of")
	if flags.asOfPairsQuery {
		switch {
		case hasQuery && !hasAsOf:
			return "", errors.New("--query needs --as-of")
		case hasAsOf && !hasQuery:
			return "", errors.New("--as-of goes with --query")
		}
	}
	if !hasAsOf {
		return "", nil
	}
	if flags.asOf == "" {
		return "", errors.New("--as-of is empty; pass a pair UUID")
	}
	return parseServiceID(flags.asOf)
}

// addTo checks the flags and adds them to a route's query string as q,
// exclude and as_of.
func (flags *trafficQueryFlags) addTo(cmd *cobra.Command, values url.Values) error {
	asOf, err := flags.asOfID(cmd)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("query") {
		values.Set("q", flags.query)
	}
	for _, value := range flags.exclude {
		values.Add("exclude", value)
	}
	if asOf != "" {
		values.Set("as_of", asOf)
	}
	return nil
}
