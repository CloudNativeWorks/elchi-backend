package extractors

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// RouteMatch represents route matching conditions
type RouteMatch struct {
	Path                string            `json:"path,omitempty"`
	Prefix              string            `json:"prefix,omitempty"`
	PathSeparatedPrefix string            `json:"path_separated_prefix,omitempty"`
	Regex               string            `json:"regex,omitempty"`
	Headers             []HeaderMatch     `json:"headers,omitempty"`
	QueryParams         []QueryParamMatch `json:"query_params,omitempty"`
	Methods             []string          `json:"methods,omitempty"`
}

// HeaderMatch represents header matching conditions
type HeaderMatch struct {
	Name         string `json:"name"`
	ExactMatch   string `json:"exact_match,omitempty"`
	RegexMatch   string `json:"regex_match,omitempty"`
	PrefixMatch  string `json:"prefix_match,omitempty"`
	SuffixMatch  string `json:"suffix_match,omitempty"`
	PresentMatch bool   `json:"present_match,omitempty"`
	InvertMatch  bool   `json:"invert_match,omitempty"`
}

// QueryParamMatch represents query parameter matching
type QueryParamMatch struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// RouteAction represents the action taken when a route matches
type RouteAction struct {
	Type            string            `json:"type"` // route, redirect, direct_response
	Cluster         string            `json:"cluster,omitempty"`
	WeightedCluster []WeightedCluster `json:"weighted_clusters,omitempty"`
	Redirect        *RedirectAction   `json:"redirect,omitempty"`
	DirectResponse  *DirectResponse   `json:"direct_response,omitempty"`
}

// WeightedCluster represents weighted cluster routing
type WeightedCluster struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
}

// RedirectAction represents redirect configuration
type RedirectAction struct {
	HostRedirect   string `json:"host_redirect,omitempty"`
	PathRedirect   string `json:"path_redirect,omitempty"`
	PrefixRewrite  string `json:"prefix_rewrite,omitempty"`
	ResponseCode   int    `json:"response_code,omitempty"`
	HTTPSRedirect  bool   `json:"https_redirect,omitempty"`
	SchemeRedirect string `json:"scheme_redirect,omitempty"`
}

// DirectResponse represents direct response configuration
type DirectResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
}

// ExtractString safely extracts a string value from interface{}
func ExtractString(data any, path string) string {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	result := gjson.GetBytes(jsonBytes, path)
	return result.String()
}

// ExtractStringArray safely extracts a string array from interface{}
func ExtractStringArray(data any, path string) []string {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	result := gjson.GetBytes(jsonBytes, path)
	if !result.IsArray() {
		return nil
	}

	var strings []string
	result.ForEach(func(_, value gjson.Result) bool {
		if str := value.String(); str != "" {
			strings = append(strings, str)
		}
		return true
	})
	return strings
}

// ExtractMap safely extracts a map from interface{}
func ExtractMap(data any, path string) map[string]any {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	result := gjson.GetBytes(jsonBytes, path)
	if !result.IsObject() {
		return nil
	}

	m := make(map[string]any)
	result.ForEach(func(key, value gjson.Result) bool {
		m[key.String()] = value.Value()
		return true
	})
	return m
}

// ExtractArray safely extracts an array from interface{}
func ExtractArray(data any, path string) []any {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil
	}

	result := gjson.GetBytes(jsonBytes, path)
	if !result.IsArray() {
		return nil
	}

	var arr []any
	result.ForEach(func(_, value gjson.Result) bool {
		arr = append(arr, value.Value())
		return true
	})
	return arr
}

// ExtractBool safely extracts a boolean value
func ExtractBool(data any, path string) bool {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return false
	}
	result := gjson.GetBytes(jsonBytes, path)
	return result.Bool()
}

// ExtractInt safely extracts an integer value
func ExtractInt(data any, path string) int {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return 0
	}
	result := gjson.GetBytes(jsonBytes, path)
	return int(result.Int())
}

// ExtractRDS extracts RDS configuration from HTTP Connection Manager
func ExtractRDS(hcmConfig any) string {
	// Primary path: resource.resource.rds.route_config_name
	rdsName := ExtractString(hcmConfig, "rds.route_config_name")
	if rdsName != "" {
		return rdsName
	}

	return ""
}

// ExtractInlineRouteConfig extracts inline route configuration
func ExtractInlineRouteConfig(hcmConfig any) map[string]any {
	// Primary path: resource.resource.route_config
	routeConfig := ExtractMap(hcmConfig, "route_config")
	if routeConfig != nil {
		return routeConfig
	}

	return nil
}

// ExtractVHDS extracts VHDS configuration from route config
// This just checks if VHDS is configured, actual VHDS names come from config_discovery
func ExtractVHDS(routeConfig any) string {
	// Check if vhds field exists
	vhdsConfig := ExtractMap(routeConfig, "vhds")
	if vhdsConfig != nil {
		return "vhds_configured" // Signal that VHDS is configured
	}

	return ""
}

// ExtractVirtualHosts extracts virtual hosts from route config
func ExtractVirtualHosts(routeConfig any) []any {
	// Direct virtual_hosts field
	vhosts := ExtractArray(routeConfig, "virtual_hosts")
	if len(vhosts) > 0 {
		return vhosts
	}

	// Alternative path
	vhosts = ExtractArray(routeConfig, "route_config.virtual_hosts")
	if len(vhosts) > 0 {
		return vhosts
	}

	return nil
}

// ExtractDomains extracts domains from virtual host
func ExtractDomains(virtualHost any) []string {
	domains := ExtractStringArray(virtualHost, "domains")
	if len(domains) > 0 {
		return domains
	}

	// Single domain case
	domain := ExtractString(virtualHost, "domain")
	if domain != "" {
		return []string{domain}
	}

	return []string{"*"}
}

// ExtractRoutes extracts routes from virtual host
func ExtractRoutes(virtualHost any) []any {
	routes := ExtractArray(virtualHost, "routes")
	return routes
}

// ExtractRouteMatch extracts match conditions from a route
func ExtractRouteMatch(route any) RouteMatch {
	match := RouteMatch{}

	// Extract match object
	matchObj := ExtractMap(route, "match")
	if matchObj == nil {
		return match
	}

	// Path matchers
	match.Path = ExtractString(matchObj, "path")
	match.Prefix = ExtractString(matchObj, "prefix")
	match.PathSeparatedPrefix = ExtractString(matchObj, "path_separated_prefix")

	// Safe regex extraction
	if regexObj := ExtractMap(matchObj, "safe_regex"); regexObj != nil {
		match.Regex = ExtractString(regexObj, "regex")
	} else {
		match.Regex = ExtractString(matchObj, "regex")
	}

	// Headers
	headersArray := ExtractArray(matchObj, "headers")
	for _, h := range headersArray {
		header := HeaderMatch{
			Name: ExtractString(h, "name"),
		}

		// Extract different match types
		header.ExactMatch = ExtractString(h, "exact_match")
		header.RegexMatch = ExtractString(h, "regex_match")
		header.PrefixMatch = ExtractString(h, "prefix_match")
		header.SuffixMatch = ExtractString(h, "suffix_match")
		header.PresentMatch = ExtractBool(h, "present_match")
		header.InvertMatch = ExtractBool(h, "invert_match")

		match.Headers = append(match.Headers, header)
	}

	// Query parameters
	queryParamsArray := ExtractArray(matchObj, "query_parameters")
	for _, qp := range queryParamsArray {
		queryParam := QueryParamMatch{
			Name:  ExtractString(qp, "name"),
			Value: ExtractString(qp, "value"),
		}
		match.QueryParams = append(match.QueryParams, queryParam)
	}

	// Methods (HTTP verbs)
	methodsArray := ExtractArray(matchObj, "headers")
	for _, h := range methodsArray {
		if ExtractString(h, "name") == ":method" {
			method := ExtractString(h, "exact_match")
			if method != "" {
				match.Methods = append(match.Methods, method)
			}
		}
	}

	return match
}

// CreateMatchDescription creates a human-readable description for match conditions
func CreateMatchDescription(match RouteMatch) string {
	var conditions []string

	// Path matchers
	if match.Path != "" {
		conditions = append(conditions, fmt.Sprintf("path: %s", match.Path))
	}
	if match.Prefix != "" {
		conditions = append(conditions, fmt.Sprintf("prefix: %s", match.Prefix))
	}
	if match.PathSeparatedPrefix != "" {
		conditions = append(conditions, fmt.Sprintf("path_separated_prefix: %s", match.PathSeparatedPrefix))
	}
	if match.Regex != "" {
		conditions = append(conditions, fmt.Sprintf("regex: %s", match.Regex))
	}

	// Methods
	if len(match.Methods) > 0 {
		conditions = append(conditions, fmt.Sprintf("methods: %s", strings.Join(match.Methods, "|")))
	}

	// Headers
	for _, header := range match.Headers {
		var headerDesc string
		if header.ExactMatch != "" {
			headerDesc = fmt.Sprintf("header %s=%s", header.Name, header.ExactMatch)
		} else if header.PrefixMatch != "" {
			headerDesc = fmt.Sprintf("header %s^=%s", header.Name, header.PrefixMatch)
		} else if header.SuffixMatch != "" {
			headerDesc = fmt.Sprintf("header %s$=%s", header.Name, header.SuffixMatch)
		} else if header.RegexMatch != "" {
			headerDesc = fmt.Sprintf("header %s~=%s", header.Name, header.RegexMatch)
		} else if header.PresentMatch {
			headerDesc = fmt.Sprintf("header %s exists", header.Name)
		} else {
			headerDesc = fmt.Sprintf("header %s", header.Name)
		}

		if header.InvertMatch {
			headerDesc = "!" + headerDesc
		}

		conditions = append(conditions, headerDesc)
	}

	// Query parameters
	for _, qp := range match.QueryParams {
		if qp.Value != "" {
			conditions = append(conditions, fmt.Sprintf("query %s=%s", qp.Name, qp.Value))
		} else {
			conditions = append(conditions, fmt.Sprintf("query %s", qp.Name))
		}
	}

	if len(conditions) == 0 {
		return "any request"
	}

	return strings.Join(conditions, " && ")
}

// ExtractRouteName extracts route name
func ExtractRouteName(route any) string {
	name := ExtractString(route, "name")
	if name != "" {
		return name
	}

	// Try to generate a name from match conditions
	match := ExtractRouteMatch(route)
	if match.Path != "" {
		return fmt.Sprintf("path_%s", match.Path)
	}
	if match.Prefix != "" {
		return fmt.Sprintf("prefix_%s", match.Prefix)
	}
	if match.PathSeparatedPrefix != "" {
		return fmt.Sprintf("path_sep_prefix_%s", match.PathSeparatedPrefix)
	}
	if match.Regex != "" {
		return "regex_route"
	}

	return "unnamed_route"
}

// ExtractRouteAction extracts the action from a route
func ExtractRouteAction(route any) RouteAction {
	action := RouteAction{}

	// Check for route (cluster routing)
	if routeObj := ExtractMap(route, "route"); routeObj != nil {
		// Single cluster
		if cluster := ExtractString(routeObj, "cluster"); cluster != "" {
			action.Type = "route"
			action.Cluster = cluster
			return action
		}

		// Weighted clusters
		if weightedClusters := ExtractArray(routeObj, "weighted_clusters.clusters"); len(weightedClusters) > 0 {
			action.Type = "route"
			for _, wc := range weightedClusters {
				weighted := WeightedCluster{
					Name:   ExtractString(wc, "name"),
					Weight: ExtractInt(wc, "weight"),
				}
				action.WeightedCluster = append(action.WeightedCluster, weighted)
			}
			return action
		}

		// Cluster header
		if clusterHeader := ExtractString(routeObj, "cluster_header"); clusterHeader != "" {
			action.Type = "route"
			action.Cluster = fmt.Sprintf("cluster_header:%s", clusterHeader)
			return action
		}
	}

	// Check for redirect
	if redirectObj := ExtractMap(route, "redirect"); redirectObj != nil {
		action.Type = "redirect"
		action.Redirect = &RedirectAction{
			HostRedirect:   ExtractString(redirectObj, "host_redirect"),
			PathRedirect:   ExtractString(redirectObj, "path_redirect"),
			PrefixRewrite:  ExtractString(redirectObj, "prefix_rewrite"),
			ResponseCode:   ExtractInt(redirectObj, "response_code"),
			HTTPSRedirect:  ExtractBool(redirectObj, "https_redirect"),
			SchemeRedirect: ExtractString(redirectObj, "scheme_redirect"),
		}
		return action
	}

	// Check for direct response
	if directResponseObj := ExtractMap(route, "direct_response"); directResponseObj != nil {
		action.Type = "direct_response"
		action.DirectResponse = &DirectResponse{
			Status: ExtractInt(directResponseObj, "status"),
			Body:   ExtractString(directResponseObj, "body.inline_string"),
		}
		return action
	}

	return action
}

// ExtractVHDSNamesFromConfigDiscovery extracts VHDS virtual host names from config_discovery array
func ExtractVHDSNamesFromConfigDiscovery(configDiscovery []any) []string {
	var vhdsNames []string

	for _, cd := range configDiscovery {
		category := ExtractString(cd, "category")
		if category == "virtual_host" {
			name := ExtractString(cd, "name")
			if name != "" {
				vhdsNames = append(vhdsNames, name)
			}
		}
	}

	return vhdsNames
}
