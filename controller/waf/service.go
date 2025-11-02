package waf

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pkgwaf "github.com/CloudNativeWorks/elchi-backend/pkg/waf"
)

const (
	WAFDataDir = "pkg/waf/data"
)

// WAFService handles WAF business logic
type WAFService struct {
	DataDir string
}

// NewWAFService creates a new WAF service
func NewWAFService() *WAFService {
	return &WAFService{
		DataDir: WAFDataDir,
	}
}

// LoadCRSRules loads CRS rules from embedded data or falls back to file
func (s *WAFService) LoadCRSRules(version string) ([]models.CRSRule, *CRSMetadata, error) {
	var data []byte
	var err error

	// Try embedded data first
	switch version {
	case "4.14.0":
		data = pkgwaf.EmbeddedRules4140
	default:
		return nil, nil, fmt.Errorf("version %s not embedded: %w", version, err)
	}

	var rules []models.CRSRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, nil, fmt.Errorf("failed to parse rules JSON: %w", err)
	}

	// Load metadata
	metadata, err := s.LoadCRSMetadata(version)
	if err != nil {
		// Metadata is optional, create default
		return rules, &CRSMetadata{
			CRSVersion: version,
			TotalRules: len(rules),
		}, nil
	}

	return rules, metadata, nil
}

// LoadCRSMetadata loads metadata for a specific CRS version
func (s *WAFService) LoadCRSMetadata(version string) (*CRSMetadata, error) {
	var data []byte
	var err error

	// Try embedded data first
	switch version {
	case "4.14.0":
		data = pkgwaf.EmbeddedMetadata4140
	default:
		return nil, fmt.Errorf("version %s not embedded: %w", version, err)
	}

	var metadata CRSMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}

	return &metadata, nil
}

// GetAvailableVersions returns list of available embedded CRS versions
func (s *WAFService) GetAvailableVersions() ([]CRSMetadata, error) {
	var versions []CRSMetadata

	// Only return embedded versions
	embeddedVersions := []string{"4.14.0"}
	for _, version := range embeddedVersions {
		if metadata, err := s.LoadCRSMetadata(version); err == nil {
			versions = append(versions, *metadata)
		}
	}

	return versions, nil
}

// FilterRules filters rules based on provided filters
func (s *WAFService) FilterRules(rules []models.CRSRule, filters RuleFilters) []models.CRSRule {
	var filtered []models.CRSRule

	for _, rule := range rules {
		// Severity filter
		if filters.Severity != "" && !strings.EqualFold(rule.Characteristics.Severity, filters.Severity) {
			continue
		}

		// Paranoia level filter
		if filters.ParanoiaLevel != nil && rule.Characteristics.ParanoiaLevel != *filters.ParanoiaLevel {
			continue
		}

		// Rule type filter
		if filters.RuleType != "" && !strings.EqualFold(rule.RuleType, filters.RuleType) {
			continue
		}

		// Tag filter - rule must have ALL specified tags
		if len(filters.Tags) > 0 {
			if !s.hasAllTags(rule, filters.Tags) {
				continue
			}
		}

		// Search term filter (search in title, description, tags, file)
		if filters.SearchTerm != "" {
			if !s.matchesSearch(rule, filters.SearchTerm) {
				continue
			}
		}

		filtered = append(filtered, rule)
	}

	return filtered
}

// hasAllTags checks if rule has all specified tags (case-insensitive)
func (s *WAFService) hasAllTags(rule models.CRSRule, filterTags []string) bool {
	// Create a map of rule tags (lowercase) for fast lookup
	ruleTags := make(map[string]bool)
	for _, tag := range rule.Characteristics.Tags {
		ruleTags[strings.ToLower(tag)] = true
	}

	// Check if all filter tags exist in rule tags
	for _, filterTag := range filterTags {
		if !ruleTags[strings.ToLower(filterTag)] {
			return false // Missing tag
		}
	}

	return true // All tags found
}

// matchesSearch checks if rule matches search term
func (s *WAFService) matchesSearch(rule models.CRSRule, searchTerm string) bool {
	searchLower := strings.ToLower(searchTerm)

	// Search in title
	if strings.Contains(strings.ToLower(rule.Title), searchLower) {
		return true
	}

	// Search in description
	if strings.Contains(strings.ToLower(rule.Description.Short), searchLower) {
		return true
	}

	// Search in tags
	for _, tag := range rule.Characteristics.Tags {
		if strings.Contains(strings.ToLower(tag), searchLower) {
			return true
		}
	}

	// Search in file name
	if strings.Contains(strings.ToLower(rule.Characteristics.File), searchLower) {
		return true
	}

	return false
}

// FindRuleByID finds a specific rule by ID
func (s *WAFService) FindRuleByID(rules []models.CRSRule, ruleID int) *models.CRSRule {
	for _, rule := range rules {
		if rule.Characteristics.ID == ruleID {
			return &rule
		}
	}
	return nil
}
