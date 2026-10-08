package compass

import (
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
// A Scope must not be copied after first use. The exported fields are retained
// for initialization compatibility; use methods to access a live Scope.
type Scope struct {
	mu           sync.RWMutex
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
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.RLock()
	defer s.mu.RUnlock()
	var host, url string
	switch v := input.(type) {
	case *http.Request:
		host = v.Host
		url = v.URL.String()
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

// MatchesWithRule determines whether input is in scope and returns the first
// matching rule, if any.
func (s *Scope) MatchesWithRule(request *http.Request) (bool, *Rule) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	host, url := request.Host, request.URL.String()
	for _, rule := range s.ExcludeRules {
		if rule.matches(host, url) {
			matched := rule
			return false, &matched
		}
	}

	for _, rule := range s.IncludeRules {
		if rule.matches(host, url) {
			matched := rule
			return true, &matched
		}
	}
	return s.DefaultAllow, nil
}

// SetDefaultAllow changes the policy for inputs that match no rule.
func (s *Scope) SetDefaultAllow(allow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.DefaultAllow = allow
}

// Snapshot returns independent rule maps and the default policy from one read.
func (s *Scope) Snapshot() (includeRules, excludeRules map[string]Rule, defaultAllow bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return maps.Clone(s.IncludeRules), maps.Clone(s.ExcludeRules), s.DefaultAllow
}

func (rule Rule) matches(host, url string) bool {
	switch rule.MatchType {
	case "host":
		return rule.Pattern.MatchString(host)
	case "url":
		return rule.Pattern.MatchString(url)
	default:
		return false
	}
}
