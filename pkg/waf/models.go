// Package waf provides Web Application Firewall data structures
// and embedded rule definitions for ModSecurity Core Rule Set.
package waf

// Rule represents a parsed ModSecurity Core Rule Set rule
type Rule struct {
	Title             string          `json:"title"`
	Description       Description     `json:"description"`
	CRSVersion        string          `json:"crs_version"`
	References        []string        `json:"references"`
	RuleType          string          `json:"rule_type"` // blocking / detection / admin-scaffolding / config
	BuildInstructions string          `json:"build_instructions"`
	LinkToRuleTests   string          `json:"link_to_rule_tests"`
	ExamplePayloads   ExamplePayloads `json:"example_payloads"`
	Characteristics   Characteristics `json:"characteristics"`
}

// Description contains various descriptions of the rule
type Description struct {
	Short       string `json:"short"`
	Extended    string `json:"extended"`
	Exploit     string `json:"exploit"`
	Rule        string `json:"rule"`
	RuleLink    string `json:"rulelink"`
	RuleLogic   string `json:"rule_logic"`
	TargetsInfo string `json:"targets_info"`
}

// ExamplePayloads contains positive and negative test cases
type ExamplePayloads struct {
	Positive []string `json:"positive"`
	Negative []string `json:"negative"`
}

// Characteristics contains the technical details of the rule
type Characteristics struct {
	ID              int        `json:"id"`
	Phase           int        `json:"phase"`
	ParanoiaLevel   int        `json:"paranoia_level"`
	Severity        string     `json:"severity"`
	File            string     `json:"file"`
	Tags            []string   `json:"tags"`
	Siblings        []int      `json:"siblings"`
	Targets         [][]string `json:"targets"`
	Operator        []string   `json:"operator"`
	Transformations []string   `json:"transformations"`
	Capec           []string   `json:"capec"`
	Chained         string     `json:"chained"` // Yes / No
}
