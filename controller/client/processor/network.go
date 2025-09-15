package processor

import (
	"fmt"
	"strings"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

type NetworkProcessor struct {
	Logger *logger.Logger
}

func (p *NetworkProcessor) ValidateAndTransform(op models.OperationClass, requestDetails models.RequestDetails, _ models.ServiceClients) (any, error) {
	// Get operation details
	operation := op.GetOperation()
	subType := operation.GetSubType()

	// Build request based on SubCommandType
	switch subType {
	case client.SubCommandType_SUB_NETPLAN_APPLY:
		return p.buildNetplanApplyRequest(op, requestDetails)
	case client.SubCommandType_SUB_ROUTE_MANAGE:
		return p.buildRouteManageRequest(op, requestDetails)
	case client.SubCommandType_SUB_POLICY_MANAGE:
		return p.buildPolicyManageRequest(op, requestDetails)
	case client.SubCommandType_SUB_GET_NETWORK_STATE:
		return p.buildNetworkStateRequest(op, requestDetails)
	case client.SubCommandType_SUB_NETPLAN_GET:
		return p.buildNetplanGetRequest(op, requestDetails)
	case client.SubCommandType_SUB_NETPLAN_ROLLBACK:
		return p.buildNetplanRollbackRequest(op, requestDetails)
	case client.SubCommandType_SUB_TABLE_MANAGE:
		return p.buildTableManageRequest(op, requestDetails)
	case client.SubCommandType_SUB_TABLE_LIST:
		return p.buildTableListRequest(op, requestDetails)
	default:
		// Legacy support - convert interfaces to netplan YAML
		return p.buildLegacyNetplanRequest(op, requestDetails)
	}
}

func (p *NetworkProcessor) buildNetplanApplyRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	netplanConfig := op.GetNetplanConfig()
	if netplanConfig == nil {
		return nil, fmt.Errorf("netplan_config is required for SUB_NETPLAN_APPLY")
	}

	// Validate YAML content
	if err := p.validateYAMLContent(netplanConfig.YamlContent); err != nil {
		return nil, fmt.Errorf("netplan YAML validation failed: %w", err)
	}

	// Enhanced safety defaults for production
	safeConfig := *netplanConfig // Copy to avoid modifying original

	// Always enforce safety in production environments
	if !safeConfig.TestMode {
		p.Logger.Warn("Netplan apply without test_mode detected - enforcing safety defaults",
			"client_id", requestDetails.ClientID,
			"project", requestDetails.Project)
		safeConfig.TestMode = true
	}

	// Always preserve controller connection
	if !safeConfig.PreserveControllerConnection {
		p.Logger.Info("Enabling connection preservation for safety",
			"client_id", requestDetails.ClientID)
		safeConfig.PreserveControllerConnection = true
	}

	// Set reasonable timeout if not provided
	if safeConfig.TestTimeoutSeconds == 0 {
		safeConfig.TestTimeoutSeconds = 10
		p.Logger.Debug("Setting default test timeout", "timeout_seconds", 10)
	} else if safeConfig.TestTimeoutSeconds > 60 {
		// Limit maximum timeout to prevent hanging operations
		p.Logger.Warn("Test timeout too high, limiting to 60 seconds",
			"requested_timeout", safeConfig.TestTimeoutSeconds)
		safeConfig.TestTimeoutSeconds = 60
	}

	service := &client.Command_Network{
		Network: &client.RequestNetwork{
			NetplanConfig: &client.NetplanConfig{
				YamlContent:                  safeConfig.YamlContent,
				TestMode:                     safeConfig.TestMode,
				TestTimeoutSeconds:           safeConfig.TestTimeoutSeconds,
				PreserveControllerConnection: safeConfig.PreserveControllerConnection,
			},
		},
	}

	p.Logger.Info("Built netplan apply request with safety measures",
		"test_mode", safeConfig.TestMode,
		"preserve_connection", safeConfig.PreserveControllerConnection,
		"timeout_seconds", safeConfig.TestTimeoutSeconds,
		"yaml_size", len(safeConfig.YamlContent))

	return service, nil
}

func (p *NetworkProcessor) buildRouteManageRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	routeOps := op.GetRouteOperations()
	if len(routeOps) == 0 {
		return nil, fmt.Errorf("route_operations are required for SUB_ROUTE_MANAGE")
	}

	// Convert to proto format with validation (matching client implementation)
	var protoRouteOps []*client.RouteOperation
	for i, routeOp := range routeOps {
		// Enhanced validation matching client-side checks
		if err := p.validateRoute(routeOp.Route, i); err != nil {
			return nil, err
		}

		var action client.RouteOperation_Action
		switch routeOp.Action {
		case "ADD":
			action = client.RouteOperation_ADD
		case "DELETE":
			action = client.RouteOperation_DELETE
		case "REPLACE":
			action = client.RouteOperation_REPLACE
		default:
			return nil, fmt.Errorf("invalid route action: %s (valid: ADD, DELETE, REPLACE)", routeOp.Action)
		}

		protoRouteOps = append(protoRouteOps, &client.RouteOperation{
			Action: action,
			Route:  routeOp.Route, // Use proto struct directly
		})

		p.Logger.Debug("Added route operation",
			"action", routeOp.Action,
			"to", routeOp.Route.To,
			"via", routeOp.Route.Via,
			"interface", routeOp.Route.Interface,
			"table", routeOp.Route.Table,
			"onlink", routeOp.Route.Onlink)
	}

	service := &client.Command_Network{
		Network: &client.RequestNetwork{
			RouteOperations: protoRouteOps,
		},
	}

	p.Logger.Info("Built route management request",
		"client_id", requestDetails.ClientID,
		"operations_count", len(protoRouteOps))

	return service, nil
}

func (p *NetworkProcessor) buildPolicyManageRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	policyOps := op.GetPolicyOperations()
	if len(policyOps) == 0 {
		return nil, fmt.Errorf("policy_operations are required for SUB_POLICY_MANAGE")
	}

	// Convert to proto format with validation (matching client implementation)
	var protoPolicyOps []*client.RoutingPolicyOperation
	for i, policyOp := range policyOps {
		// Enhanced validation matching client-side checks
		if err := p.validatePolicy(policyOp.Policy, i); err != nil {
			return nil, err
		}

		var action client.RoutingPolicyOperation_Action
		switch policyOp.Action {
		case "ADD":
			action = client.RoutingPolicyOperation_ADD
		case "DELETE":
			action = client.RoutingPolicyOperation_DELETE
		case "REPLACE":
			action = client.RoutingPolicyOperation_REPLACE
		default:
			return nil, fmt.Errorf("invalid policy action: %s (valid: ADD, DELETE, REPLACE)", policyOp.Action)
		}

		protoPolicyOps = append(protoPolicyOps, &client.RoutingPolicyOperation{
			Action: action,
			Policy: policyOp.Policy, // Use proto struct directly
		})

		p.Logger.Debug("Added policy operation",
			"action", policyOp.Action,
			"from", policyOp.Policy.From,
			"to", policyOp.Policy.To,
			"table", policyOp.Policy.Table,
			"priority", policyOp.Policy.Priority)
	}

	service := &client.Command_Network{
		Network: &client.RequestNetwork{
			PolicyOperations: protoPolicyOps,
		},
	}

	p.Logger.Info("Built policy management request",
		"client_id", requestDetails.ClientID,
		"operations_count", len(protoPolicyOps))

	return service, nil
}

func (p *NetworkProcessor) buildNetworkStateRequest(_ models.OperationClass, _ models.RequestDetails) (any, error) {
	// No additional parameters needed for network state query
	service := &client.Command_Network{
		Network: &client.RequestNetwork{},
	}

	return service, nil
}

func (p *NetworkProcessor) buildNetplanGetRequest(_ models.OperationClass, _ models.RequestDetails) (any, error) {
	// No additional parameters needed for netplan get
	service := &client.Command_Network{
		Network: &client.RequestNetwork{},
	}

	return service, nil
}

func (p *NetworkProcessor) buildNetplanRollbackRequest(_ models.OperationClass, _ models.RequestDetails) (any, error) {
	// No additional parameters needed for rollback
	service := &client.Command_Network{
		Network: &client.RequestNetwork{},
	}

	return service, nil
}

func (p *NetworkProcessor) buildTableManageRequest(op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	tableOps := op.GetTableOperations()
	if len(tableOps) == 0 {
		return nil, fmt.Errorf("table_operations are required for SUB_TABLE_MANAGE")
	}

	// Convert to proto format with validation
	var protoTableOps []*client.TableOperation
	for i, tableOp := range tableOps {
		// Validate table fields
		if tableOp.Table.Id == 0 {
			return nil, fmt.Errorf("table[%d]: table ID is required", i)
		}
		// Elchi manages tables in range 100-999 (as per client implementation)
		if tableOp.Table.Id < 100 || tableOp.Table.Id > 999 {
			return nil, fmt.Errorf("table[%d]: table ID must be between 100-999 (Elchi management range)", i)
		}
		if tableOp.Table.Name == "" {
			return nil, fmt.Errorf("table[%d]: table name is required", i)
		}

		// Validate table name format (alphanumeric and underscores only)
		if !isValidTableName(tableOp.Table.Name) {
			return nil, fmt.Errorf("table[%d]: table name '%s' contains invalid characters (only alphanumeric and underscores allowed)", i, tableOp.Table.Name)
		}

		var action client.TableOperation_Action
		switch tableOp.Action {
		case "ADD":
			action = client.TableOperation_ADD
		case "DELETE":
			action = client.TableOperation_DELETE
		case "REPLACE":
			action = client.TableOperation_REPLACE
		default:
			return nil, fmt.Errorf("invalid table action: %s (valid: ADD, DELETE, REPLACE)", tableOp.Action)
		}

		protoTableOps = append(protoTableOps, &client.TableOperation{
			Action: action,
			Table:  tableOp.Table, // Use proto struct directly
		})

		p.Logger.Debug("Added table operation",
			"action", tableOp.Action,
			"table_id", tableOp.Table.Id,
			"table_name", tableOp.Table.Name)
	}

	service := &client.Command_Network{
		Network: &client.RequestNetwork{
			TableOperations: protoTableOps,
		},
	}

	p.Logger.Info("Built table management request",
		"client_id", requestDetails.ClientID,
		"operations_count", len(protoTableOps))

	return service, nil
}

func (p *NetworkProcessor) buildTableListRequest(_ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	// No additional parameters needed for table list
	service := &client.Command_Network{
		Network: &client.RequestNetwork{},
	}

	p.Logger.Info("Built table list request", "client_id", requestDetails.ClientID)

	return service, nil
}

// buildLegacyNetplanRequest handles legacy requests by returning error
func (p *NetworkProcessor) buildLegacyNetplanRequest(_ models.OperationClass, _ models.RequestDetails) (any, error) {
	return nil, fmt.Errorf("legacy interface-based requests are no longer supported, use SUB_NETPLAN_APPLY with netplan_config instead")
}

// validateYAMLContent performs basic validation on netplan YAML content
func (p *NetworkProcessor) validateYAMLContent(yamlContent string) error {
	if yamlContent == "" {
		return fmt.Errorf("YAML content cannot be empty")
	}

	// Basic YAML structure validation
	if !contains(yamlContent, "network:") {
		return fmt.Errorf("YAML must contain 'network:' root element")
	}

	if !contains(yamlContent, "version:") {
		p.Logger.Warn("YAML missing version field - this may cause issues")
	}

	// Check for potentially dangerous configurations
	if contains(yamlContent, "dhcp4: false") && contains(yamlContent, "addresses: []") {
		p.Logger.Warn("Configuration may result in no network connectivity - interface with dhcp4 disabled and no static addresses")
	}

	return nil
}

// Helper function for string contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) &&
			(s[:len(substr)] == substr ||
				s[len(s)-len(substr):] == substr ||
				containsMiddle(s, substr))))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// isValidTableName validates routing table name format (matches client implementation)
func isValidTableName(name string) bool {
	if len(name) == 0 {
		return false
	}

	// Allow alphanumeric, underscore, and hyphen (same as client implementation)
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// validateRoute validates route parameters (matching client implementation)
func (p *NetworkProcessor) validateRoute(route *client.Route, index int) error {
	// Validate destination (to field)
	if route.To != "" && route.To != "0.0.0.0/0" {
		if !isValidCIDR(route.To) {
			return fmt.Errorf("route[%d]: invalid destination CIDR '%s'", index, route.To)
		}
	}

	// Validate gateway (via field)
	if route.Via != "" {
		if !isValidIPAddress(route.Via) {
			return fmt.Errorf("route[%d]: invalid gateway IP address '%s'", index, route.Via)
		}
	}

	// Validate source (if specified)
	if route.Source != "" {
		if !isValidIPAddress(route.Source) {
			return fmt.Errorf("route[%d]: invalid source IP address '%s'", index, route.Source)
		}
	}

	// Validate scope values
	if route.Scope != "" {
		validScopes := []string{"global", "site", "link", "host"}
		if !contains(fmt.Sprintf(" %s ", strings.Join(validScopes, " ")), fmt.Sprintf(" %s ", route.Scope)) {
			return fmt.Errorf("route[%d]: invalid scope '%s', must be one of: %s", index, route.Scope, strings.Join(validScopes, ", "))
		}
	}

	// Either gateway or interface must be specified (but not necessarily both)
	if route.Via == "" && route.Interface == "" {
		return fmt.Errorf("route[%d]: either 'via' (gateway) or 'interface' must be specified", index)
	}

	return nil
}

// validatePolicy validates policy parameters (matching client implementation)
func (p *NetworkProcessor) validatePolicy(policy *client.RoutingPolicy, index int) error {
	// Table ID validation
	if policy.Table == 0 {
		return fmt.Errorf("policy[%d]: table ID cannot be zero", index)
	}

	// Priority validation - client enforces range 100-999 for Elchi policies
	if policy.Priority == 0 {
		return fmt.Errorf("policy[%d]: priority cannot be zero", index)
	}
	if policy.Priority < 100 || policy.Priority > 999 {
		return fmt.Errorf("policy[%d]: priority must be between 100-999 for Elchi-managed policies", index)
	}

	// Validate source address (from field)
	if policy.From != "" {
		if !isValidCIDR(policy.From) {
			return fmt.Errorf("policy[%d]: invalid source address/network '%s'", index, policy.From)
		}
	}

	// Validate destination address (to field)
	if policy.To != "" {
		if !isValidCIDR(policy.To) {
			return fmt.Errorf("policy[%d]: invalid destination address/network '%s'", index, policy.To)
		}
	}

	// At least one match criterion must be specified (from, to, or interface)
	if policy.From == "" && policy.To == "" && policy.Interface == "" {
		return fmt.Errorf("policy[%d]: at least one match criterion must be specified (from, to, or interface)", index)
	}

	return nil
}

// Network validation helper functions
func isValidIPAddress(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}

	for _, part := range parts {
		if len(part) == 0 || len(part) > 3 {
			return false
		}

		num := 0
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
			num = num*10 + int(char-'0')
		}

		if num > 255 {
			return false
		}

		// No leading zeros except for "0" itself
		if len(part) > 1 && part[0] == '0' {
			return false
		}
	}

	return true
}

func isValidCIDR(cidr string) bool {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return false
	}

	// Validate IP part
	if !isValidIPAddress(parts[0]) {
		return false
	}

	// Validate prefix length
	prefixLen := 0
	for _, char := range parts[1] {
		if char < '0' || char > '9' {
			return false
		}
		prefixLen = prefixLen*10 + int(char-'0')
	}

	return prefixLen >= 0 && prefixLen <= 32
}
