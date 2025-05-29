package marasi

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
)

// Rule represents a single filtering rule in the scope system.
// It contains a compiled regular expression and the type of matching to perform.
type Rule struct {
	Pattern   *regexp.Regexp // Compiled regular expression pattern
	MatchType string         // Type of matching: "host" or "url"
}

// Scope represents the inclusion/exclusion rules and default behavior for filtering
// HTTP requests and responses. It manages sets of rules and determines whether
// traffic should be processed based on host or URL patterns.
type Scope struct {
	IncludeRules map[string]Rule // Map of inclusion rules, key format: "pattern|matchType"
	ExcludeRules map[string]Rule // Map of exclusion rules, key format: "pattern|matchType"
	DefaultAllow bool            // Default behavior for items not matching any rule
}

// NewScope creates a new Scope with the specified default behavior.
//
// Parameters:
//   - defaultAllow: Whether to allow items that don't match any rules
//
// Returns:
//   - *Scope: New scope instance with empty rule sets
func NewScope(defaultAllow bool) *Scope {
	return &Scope{
		IncludeRules: make(map[string]Rule),
		ExcludeRules: make(map[string]Rule),
		DefaultAllow: defaultAllow,
	}
}

// MatchesString determines if a given string is in scope based on matchType
func (s *Scope) MatchesString(input string, matchType string) bool {
	matchType = strings.ToLower(matchType)

	// Validate matchType
	if matchType != "host" && matchType != "url" {
		return s.DefaultAllow
	}

	target := input

	// Check exclusion rules first
	for _, rule := range s.ExcludeRules {
		if rule.MatchType != matchType {
			continue
		}
		if rule.Pattern.MatchString(target) {
			return false // Denied by exclude rule
		}
	}

	// Check inclusion rules
	for _, rule := range s.IncludeRules {
		if rule.MatchType != matchType {
			continue
		}
		if rule.Pattern.MatchString(target) {
			return true // Allowed by include rule
		}
	}

	// Default behavior
	return s.DefaultAllow
}

// ClearRules clears all inclusion and exclusion rules from the scope
func (s *Scope) ClearRules() {
	s.IncludeRules = make(map[string]Rule)
	s.ExcludeRules = make(map[string]Rule)
}

// AddRule adds a rule to the scope
func (s *Scope) AddRule(pattern, matchType string, exclude bool) error {
	matchType = strings.ToLower(matchType)
	if matchType != "host" && matchType != "url" {
		return fmt.Errorf("invalid match type: %s", matchType)
	}

	trimmedPattern := strings.TrimPrefix(pattern, "-")
	compiled, err := regexp.Compile(trimmedPattern)
	if err != nil {
		return fmt.Errorf("invalid regex pattern: %w", err)
	}
	rule := Rule{
		Pattern:   compiled,
		MatchType: matchType,
	}
	key := fmt.Sprintf("%s|%s", compiled.String(), matchType)

	if exclude {
		if _, exists := s.ExcludeRules[key]; exists {
			return fmt.Errorf("rule already exists in exclude list")
		}
		s.ExcludeRules[key] = rule
	} else {
		if _, exists := s.IncludeRules[key]; exists {
			return fmt.Errorf("rule already exists in include list")
		}
		s.IncludeRules[key] = rule
	}

	return nil
}

// RemoveRule removes a rule from the scope
func (s *Scope) RemoveRule(pattern, matchType string, exclude bool) error {
	matchType = strings.ToLower(matchType)
	key := fmt.Sprintf("%s|%s", strings.TrimPrefix(pattern, "-"), matchType)

	if exclude {
		if _, exists := s.ExcludeRules[key]; !exists {
			return fmt.Errorf("rule not found in exclude list")
		}
		delete(s.ExcludeRules, key)
	} else {
		if _, exists := s.IncludeRules[key]; !exists {
			return fmt.Errorf("rule not found in include list")
		}
		delete(s.IncludeRules, key)
	}

	return nil
}

// Matches determines if a *http.Request or *http.Response is in scope
func (s *Scope) Matches(input interface{}) bool {
	var host, url string
	switch v := input.(type) {
	case *http.Request:
		host = v.Host
		url = v.URL.String()
		log.Println(url)
	case *http.Response:
		if v.Request != nil {
			host = v.Request.Host
			url = v.Request.URL.String()
		} else {
			// If the response doesn't have an associated request, we can't proceed
			return s.DefaultAllow
		}
	default:
		// If input is not a *http.Request or *http.Response, return default behavior
		return s.DefaultAllow
	}

	// Check exclusion rules first
	for _, rule := range s.ExcludeRules {
		var target string
		switch rule.MatchType {
		case "host":
			target = host
		case "url":
			target = url
		default:
			continue // Skip unknown match types
		}
		if rule.Pattern.MatchString(target) {
			return false // Denied by exclude rule
		}
	}

	// Check inclusion rules
	for _, rule := range s.IncludeRules {
		var target string
		switch rule.MatchType {
		case "host":
			target = host
		case "url":
			target = url
		default:
			continue // Skip unknown match types
		}
		if rule.Pattern.MatchString(target) {
			return true // Allowed by include rule
		}
	}

	// Default behavior
	return s.DefaultAllow
}
