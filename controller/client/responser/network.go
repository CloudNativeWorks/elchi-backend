package responser

import (
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

type NetworkResponser struct{}

func (p *NetworkResponser) ValidateAndTransform(op models.OperationClass, response *pb.CommandResponse) any {
	// Enhanced response processing for new network operations
	if response.GetNetwork() == nil {
		return response
	}

	networkResponse := response.GetNetwork()

	// Create enhanced response structure
	result := map[string]any{
		"success": response.Success,
		"error":   response.Error,
	}

	// Get message from the network response, not the main response
	if networkResponse.Message != "" {
		result["message"] = networkResponse.Message
	}

	// Add network-specific response data
	if networkResponse.NetworkState != nil {
		result["network_state"] = map[string]any{
			"interfaces":           networkResponse.NetworkState.Interfaces,
			"routes":               networkResponse.NetworkState.Routes,
			"policies":             networkResponse.NetworkState.Policies,
			"routing_tables":       networkResponse.NetworkState.RoutingTables,
			"current_netplan_yaml": networkResponse.NetworkState.CurrentNetplanYaml,
		}
	}

	// Add current YAML if available
	if networkResponse.CurrentYaml != "" {
		result["current_yaml"] = networkResponse.CurrentYaml
	}

	// Add operation-specific information
	if op != nil {
		operation := op.GetOperation()
		if operation != nil {
			subType := operation.GetSubType()
			result["operation_type"] = subType.String()

			// Add safety information for netplan operations
			switch subType {
			case pb.SubCommandType_SUB_NETPLAN_APPLY:
				if op.GetNetplanConfig() != nil {
					config := op.GetNetplanConfig()
					result["safety_info"] = map[string]any{
						"test_mode":                      config.TestMode,
						"preserve_controller_connection": config.PreserveControllerConnection,
						"test_timeout_seconds":           config.TestTimeoutSeconds,
					}
				}
			case pb.SubCommandType_SUB_ROUTE_MANAGE:
				routeOps := op.GetRouteOperations()
				if len(routeOps) > 0 {
					result["route_operations_count"] = len(routeOps)
				}
			case pb.SubCommandType_SUB_POLICY_MANAGE:
				policyOps := op.GetPolicyOperations()
				if len(policyOps) > 0 {
					result["policy_operations_count"] = len(policyOps)
				}
			case pb.SubCommandType_SUB_TABLE_MANAGE:
				tableOps := op.GetTableOperations()
				if len(tableOps) > 0 {
					result["table_operations_count"] = len(tableOps)
				}
			case pb.SubCommandType_SUB_TABLE_LIST:
				result["operation_info"] = "Table list request"
			}
		}
	}

	// Enhanced rollback detection with more details
	message := networkResponse.Message
	if !response.Success {
		if contains(response.Error, "rollback") || contains(message, "rollback") {
			result["rollback_triggered"] = true
			result["rollback_reason"] = "Network configuration failed, automatic rollback performed"
		}
		if contains(response.Error, "connection_lost") || contains(response.Error, "connection lost") {
			result["connection_lost"] = true
			result["safety_triggered"] = true
		}
		if contains(response.Error, "test_failed") || contains(response.Error, "test failed") {
			result["test_failed"] = true
			result["test_mode_protected"] = true
		}
	}

	// Enhanced connection status detection
	if contains(message, "connection preserved") || contains(message, "connection maintained") {
		result["connection_preserved"] = true
	}

	// Test mode status
	if contains(message, "test_mode") {
		result["test_mode_applied"] = true
	}

	// Success indicators for safety features
	if response.Success {
		if contains(message, "successfully applied") && contains(message, "test") {
			result["safely_applied"] = true
		}
		if contains(message, "backed up") || contains(message, "backup created") {
			result["backup_created"] = true
		}
	}

	return result
}

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
