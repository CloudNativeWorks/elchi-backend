package dependency

func (h *AppHandler) AddNode(node Node) {
	if node.ID == "" || node.Name == "" {
		h.Logger.Debugf("An empty or missing value node detected, not added: %+v\n", node)
		return
	}

	if h.isNodeAlreadyAdded(node.ID) {
		h.Logger.Debugf("Node already added: %s\n", node.ID)
		return
	}

	dependency := Dependency{
		Data: struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			Category  string `json:"category"`
			Gtype     string `json:"gtype"`
			Link      string `json:"link"`
			First     bool   `json:"first"`
			Direction string `json:"direction"`
		}{
			ID:        node.ID,
			Label:     node.Name,
			Category:  node.Collection,
			Gtype:     node.Gtype.String(),
			Link:      node.Link,
			First:     node.First,
			Direction: node.Direction,
		},
	}

	h.Logger.Debugf("Adding node: %+v\n", node)
	h.Dependencies.Nodes = append(h.Dependencies.Nodes, dependency)
}

func (h *AppHandler) AddNodeAndEdge(source Node, target Depend, isUpstream bool) {
	var edge Edge
	if isUpstream {
		edge = Edge{
			Data: struct {
				Source string `json:"source"`
				Target string `json:"target"`
				Label  string `json:"label"`
			}{
				Source: source.ID,
				Target: target.ID,
				Label:  source.Gtype.PrettyName() + " to " + target.Gtype.PrettyName(),
			},
		}
	} else {
		edge = Edge{
			Data: struct {
				Source string `json:"source"`
				Target string `json:"target"`
				Label  string `json:"label"`
			}{
				Source: target.ID,
				Target: source.ID,
				Label:  target.Gtype.PrettyName() + " to " + source.Gtype.PrettyName(),
			},
		}
	}

	if edge.Data.Source != edge.Data.Target && !h.isEdgeAlreadyAdded(edge.Data.Source, edge.Data.Target) {
		h.Logger.Debugf("Adding edge: %+v\n", edge)
		h.Dependencies.Edges = append(h.Dependencies.Edges, edge)
	} else {
		h.Logger.Debugf("Skipping self or existing edge: %+v\n", edge)
	}
}

func (h *AppHandler) isNodeAlreadyAdded(nodeID string) bool {
	for _, node := range h.Dependencies.Nodes {
		if node.Data.ID == nodeID {
			return true
		}
	}
	return false
}

func (h *AppHandler) isEdgeAlreadyAdded(source, target string) bool {
	for _, edge := range h.Dependencies.Edges {
		if edge.Data.Source == source && edge.Data.Target == target {
			return true
		}
	}
	return false
}
