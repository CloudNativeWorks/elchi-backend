package envoys

import "strings"

// GetNodeIDParts parses a nodeID into its components.
// NodeID format: "name::project::downstreamAddress" or "name::project"
// Returns: (name, project, downstreamAddress)
func GetNodeIDParts(nodeID string) (name, project, downstreamAddress string) {
	parts := strings.Split(nodeID, "::")
	switch len(parts) {
	case 2:
		return parts[0], parts[1], ""
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		return "", "", ""
	}
}
