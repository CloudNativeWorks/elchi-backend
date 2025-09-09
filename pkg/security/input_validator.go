package security

import (
	"regexp"
	"strings"
)

// IsValidSearchInput validates search input against ReDoS patterns
func IsValidSearchInput(input string) bool {
	input = strings.TrimSpace(input)
	if input == "" {
		return true
	}
	
	if len(input) > 100 {
		return false
	}
	
	// Check for ReDoS patterns using regex matching
	// These patterns can cause catastrophic backtracking
	redosPatterns := []string{
		`\(\.\*\)\+`,         // (.*)+ - specific dangerous pattern
		`\(\.\*\)\*`,         // (.*)* - specific dangerous pattern  
		`\(.+\)\+`,           // (anything)+
		`\(.+\)\*`,           // (anything)*
		`\^\(.+\)\+\$`,       // ^(anything)+$
		`\^\(.+\)\*\$`,       // ^(anything)*$
		`\[.*\]\*`,           // [anything]*
		`.+\*.+\*`,           // anything*anything*
		`.+\{.+,.+\}`,        // anything{n,m}  
		`.+\+.+\+`,           // anything+anything+
	}
	
	for _, pattern := range redosPatterns {
		if matched, _ := regexp.MatchString(pattern, input); matched {
			return false
		}
	}
	
	// Direct string checks for common ReDoS patterns  
	dangerousSubstrings := []string{
		"(.*)+",
		"(.*)*",
		"(.*)+ ",
		"(.*)* ",
		"(.*){",
		"[^]*",
		")+",
		")*",
		"(a+)+",
		"(a*)*",
		"(.*)+$",
		"^(.*)+",
		"(.+)+",
		"(.+)*",
		"(a+)*",
		"a*a*",
		"a+a+",
		".*.*",
		".+.+",
	}
	
	for _, substr := range dangerousSubstrings {
		if strings.Contains(input, substr) {
			return false
		}
	}
	
	// Check for excessive quantifiers
	plusCount := strings.Count(input, "+")
	starCount := strings.Count(input, "*")
	parenCount := strings.Count(input, "(")
	
	if plusCount > 2 || starCount > 1 || parenCount > 2 {
		return false
	}
	
	return true
}

// SanitizeSearchInput sanitizes search input for safe usage
func SanitizeSearchInput(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	
	// Length limit to prevent complex patterns
	if len(input) > 100 {
		input = input[:100]
	}
	
	if !IsValidSearchInput(input) {
		return "", false
	}
	
	// For simple alphanumeric searches, don't escape (keeps it user-friendly)
	// Only escape if it contains regex special characters
	if strings.ContainsAny(input, ".*+?^${}()|[]\\") {
		// Escape dangerous characters but preserve basic functionality
		replacements := map[string]string{
			"*": "\\*",
			"+": "\\+",
			"?": "\\?",
			"^": "\\^",
			"$": "\\$",
			"{": "\\{",
			"}": "\\}",
			"(": "\\(",
			")": "\\)",
			"|": "\\|",
			"[": "\\[",
			"]": "\\]",
			"\\": "\\\\",
		}
		
		for char, replacement := range replacements {
			input = strings.ReplaceAll(input, char, replacement)
		}
	}
	
	return input, true
}