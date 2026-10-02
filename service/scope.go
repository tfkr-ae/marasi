package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tfkr-ae/marasi"
)

type scopeCheckRequest struct {
	URL *string `json:"url"`
}

type scopeCheckResponse struct {
	InScope        bool            `json:"in_scope"`
	TestedURL      string          `json:"tested_url"`
	Rule           *scopeCheckRule `json:"rule"`
	CompassEnabled bool            `json:"compass_enabled"`
}

type scopeCheckRule struct {
	Pattern   string `json:"pattern"`
	MatchType string `json:"match_type"`
}

var errInvalidScopeRequest = errors.New("invalid scope request")

func addScopeRoutes(mux routeMux, proxy *marasi.Proxy) {
	mux.HandleFunc("POST /scope/check", func(w http.ResponseWriter, r *http.Request) {
		urlInput, err := decodeScopeCheckRequest(r)
		if err != nil {
			writeScopeCheckError(w, r, http.StatusBadRequest, "invalid_scope_request")
			return
		}

		parsedURL, ok := parseScopeCheckURL(urlInput)
		if !ok {
			writeScopeCheckError(w, r, http.StatusBadRequest, "bad_request")
			return
		}

		if proxy == nil || proxy.Scope == nil {
			writeScopeCheckError(w, r, http.StatusNotFound, "not_found")
			return
		}

		request := &http.Request{URL: parsedURL, Host: parsedURL.Host}
		inScope, matchedRule := proxy.Scope.MatchesWithRule(request)
		var rule *scopeCheckRule
		if matchedRule != nil {
			rule = &scopeCheckRule{
				Pattern:   matchedRule.Pattern.String(),
				MatchType: matchedRule.MatchType,
			}
		}

		compassEnabled := false
		if extension, ok := proxy.GetExtension("compass"); ok {
			compassEnabled = extension.MetadataSnapshot().Enabled
		}

		writeJSON(w, r, http.StatusOK, scopeCheckResponse{
			InScope:        inScope,
			TestedURL:      parsedURL.String(),
			Rule:           rule,
			CompassEnabled: compassEnabled,
		})
	})
}

func decodeScopeCheckRequest(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errInvalidScopeRequest
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request scopeCheckRequest
	if err := decoder.Decode(&request); err != nil || request.URL == nil {
		return "", errInvalidScopeRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errInvalidScopeRequest
	}
	return *request.URL, nil
}

func parseScopeCheckURL(input string) (*url.URL, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, false
	}
	parsedURL, err := url.Parse(input)
	if err != nil || parsedURL.Scheme == "" {
		parsedURL, err = url.Parse("https://" + input)
		if err != nil {
			return nil, false
		}
	}
	return parsedURL, true
}

func writeScopeCheckError(w http.ResponseWriter, r *http.Request, status int, code string) {
	writeJSON(w, r, status, struct {
		Error string `json:"error"`
	}{Error: code})
}
