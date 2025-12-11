package dependency

import (
	"context"
	"fmt"
)

func (h *AppHandler) ProcessResource(ctx context.Context, activeResource Depend, version string) {
	h.SetVersion(version)
	visitedUpstream := make(map[string]bool)
	h.processUpstream(ctx, activeResource, visitedUpstream)

	visitedDownstream := make(map[string]bool)
	h.processDownstream(ctx, activeResource, visitedDownstream)
}

func generateUniqueKey(resource Depend) string {
	return fmt.Sprintf("%s_%s_%s_%s", resource.Name, resource.Gtype, resource.Collection, resource.Project)
}

func (h *AppHandler) processUpstream(ctx context.Context, activeResource Depend, visited map[string]bool) {
	uniqueKey := generateUniqueKey(activeResource)
	if visited[uniqueKey] {
		return
	}

	visited[uniqueKey] = true
	node, upstreams := h.CallUpstreamFunction(ctx, activeResource)
	if node.ID != "" && node.Name != "" && node.Gtype != "" {
		h.AddNode(node)
		activeResource.First = false
	} else {
		h.Logger.Infof("Node is missing required fields, not adding: %+v\n", node)
	}

	for _, up := range upstreams {
		if up.ID != "" && up.Name != "" && up.Gtype != "" {
			h.AddNodeAndEdge(node, up, true)
			h.processUpstream(ctx, up, visited)
		} else {
			h.Logger.Infof("Upstream is missing required fields, not adding: %+v\n", up)
		}
	}
}

func (h *AppHandler) processDownstream(ctx context.Context, activeResource Depend, visited map[string]bool) {
	uniqueKey := generateUniqueKey(activeResource)
	if visited[uniqueKey] {
		return
	}

	visited[uniqueKey] = true

	node, downstreams := h.CallDownstreamFunction(ctx, activeResource)
	if h.isNodeValid(node) {
		h.AddNode(node)
		activeResource.First = false
	} else {
		h.Logger.Infof("Node is missing required fields, not adding: %+v\n", node)
	}

	for _, down := range downstreams {
		if h.isValidDownstream(node, down) {
			h.AddNodeAndEdge(node, down, false)
			h.processDownstream(ctx, down, visited)
		} else {
			h.Logger.Infof("Downstream is missing required fields, not directly connected, or from incorrect source, not adding: %+v\n", down)
		}
	}
}

func (h *AppHandler) isNodeValid(node Node) bool {
	return node.ID != "" && node.Name != "" && node.Gtype != ""
}

func (h *AppHandler) isValidDownstream(node Node, down Depend) bool {
	return down.ID != "" && down.Name != "" && down.Gtype != "" &&
		down.Direction == "downstream" && down.Source == node.ID
}
