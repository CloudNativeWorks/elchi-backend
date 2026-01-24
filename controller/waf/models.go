package waf

import "github.com/CloudNativeWorks/elchi-backend/pkg/models"

// CRSRulesResponse represents the API response for CRS rules
type CRSRulesResponse struct {
	CorazaVersion string           `json:"coraza_version"`
	CRSVersion    string           `json:"crs_version"`
	TotalRules    int              `json:"total_rules"`
	FilteredRules int              `json:"filtered_rules,omitempty"`
	Rules         []models.CRSRule `json:"rules"`
}

// CRSMetadata represents metadata about available CRS versions
type CRSMetadata struct {
	CorazaVersion string `json:"coraza_version"`
	CRSVersion    string `json:"crs_version"`
	TotalRules    int    `json:"total_rules"`
	GeneratedAt   string `json:"generated_at"`
}

// CRSVersionsResponse represents available CRS versions
type CRSVersionsResponse struct {
	Versions []CRSMetadata `json:"versions"`
}

// RuleFilters contains filter parameters for rule queries
type RuleFilters struct {
	Severity      string
	ParanoiaLevel *int
	RuleType      string
	SearchTerm    string
	Tags          []string // Tag filter (multiple tags can be provided)
}
