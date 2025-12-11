package analysis

import (
	"context"
	"fmt"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/poker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/async/job"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/helper"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
	"github.com/CloudNativeWorks/elchi-backend/pkg/resources"
)

// Analyzer implements dependency analysis for resources
type Analyzer struct {
	dbContext *db.AppContext
	logger    *logger.Logger
}

// AnalysisRequest represents a request to analyze dependencies
type AnalysisRequest struct {
	ResourceType models.GType `json:"resource_type"`
	ResourceName string       `json:"resource_name"`
	Project      string       `json:"project"`
	Version      string       `json:"version"`
	Action       string       `json:"action"`
}

// DependencyAnalysis represents the result of dependency analysis
type DependencyAnalysis struct {
	SourceResource    *job.SourceResource `json:"source_resource"`
	AffectedListeners []string            `json:"affected_listeners"`
	DependencyChain   []string            `json:"dependency_chain"`
	DurationMS        int64               `json:"duration_ms"`
}

// NewAnalyzer creates a new dependency analyzer
func NewAnalyzer(dbContext *db.AppContext, l *logger.Logger) *Analyzer {
	if l == nil {
		l = logger.NewLogger("dependency-analyzer")
	}

	return &Analyzer{
		dbContext: dbContext,
		logger:    l,
	}
}

// Analyze analyzes dependencies for a resource
func (a *Analyzer) Analyze(ctx context.Context, req *AnalysisRequest) (*DependencyAnalysis, error) {
	startTime := time.Now()

	// Initialize result
	result := &DependencyAnalysis{
		SourceResource: &job.SourceResource{
			Type:       req.ResourceType.String(),
			Name:       req.ResourceName,
			Collection: req.ResourceType.CollectionString(),
			Action:     job.ActionType(req.Action),
			ProjectID:  req.Project,
			Version:    req.Version,
		},
		AffectedListeners: []string{},
		DependencyChain:   []string{},
	}

	// If resource is a listener, it only affects itself
	if req.ResourceType == models.Listener {
		result.AffectedListeners = append(result.AffectedListeners, req.ResourceName)
		result.DurationMS = time.Since(startTime).Milliseconds()
		return result, nil
	}

	// Collect all affected listeners through dependency chain
	uniqueListeners := make(map[string]bool)
	dependencyChain := []string{}

	// Use the same downstream filter logic as existing system
	a.collectListenersRecursively(
		ctx,
		req.ResourceType,
		req.ResourceName,
		req.Project,
		req.Version,
		uniqueListeners,
		&dependencyChain,
		0, // depth
	)

	// Convert map to slice
	for listener := range uniqueListeners {
		result.AffectedListeners = append(result.AffectedListeners, listener)
	}

	result.DependencyChain = dependencyChain
	result.DurationMS = time.Since(startTime).Milliseconds()

	a.logger.Infof("Dependency analysis for %s %s found %d affected listeners in %dms",
		req.ResourceType, req.ResourceName, len(result.AffectedListeners), result.DurationMS)

	return result, nil
}

// collectListenersRecursively collects all affected listeners recursively
func (a *Analyzer) collectListenersRecursively(
	ctx context.Context,
	gtype models.GType,
	resourceName string,
	project string,
	version string,
	listeners map[string]bool,
	dependencyChain *[]string,
	depth int,
) {
	// Prevent infinite recursion
	if depth > 10 {
		a.logger.Warnf("Max recursion depth reached for %s %s", gtype, resourceName)
		return
	}

	// Add to dependency chain
	pathWithGtype := fmt.Sprintf("%s===%s", gtype.String(), resourceName)

	// Check if already processed to avoid cycles
	if helper.Contains(*dependencyChain, pathWithGtype) {
		return
	}

	*dependencyChain = append(*dependencyChain, pathWithGtype)

	// If this is a listener, add it to results
	if gtype == models.Listener {
		listeners[resourceName] = true
		return
	}

	// Use same downstream filter logic as existing system
	dfm := downstreamfilters.DownstreamFilter{
		Name:    resourceName,
		Project: project,
		Version: version,
	}

	// Get downstream filters for this resource type
	filterResults := gtype.DownstreamFilters(dfm)
	if filterResults == nil {
		return
	}

	// Process each filter result
	for _, filterResult := range filterResults {
		// Get affected resources for this filter
		rGeneral, err := resources.GetGenerals(ctx, a.dbContext, filterResult.Collection, filterResult.Filter)
		if err != nil {
			a.logger.Debugf("Error getting resources for collection %s: %v", filterResult.Collection, err)
			continue
		}

		// Recursively process each affected resource
		for _, general := range rGeneral {
			a.collectListenersRecursively(
				ctx,
				general.GType,
				general.Name,
				project,
				version,
				listeners,
				dependencyChain,
				depth+1,
			)
		}
	}
}

// Alternative simpler implementation using existing poker logic
func (a *Analyzer) AnalyzeUsingPoker(ctx context.Context, req *AnalysisRequest) (*DependencyAnalysis, error) {
	startTime := time.Now()

	// Initialize result
	result := &DependencyAnalysis{
		SourceResource: &job.SourceResource{
			Type:       req.ResourceType.String(),
			Name:       req.ResourceName,
			Collection: req.ResourceType.CollectionString(),
			Action:     job.ActionType(req.Action),
			ProjectID:  req.Project,
			Version:    req.Version,
		},
		AffectedListeners: []string{},
		DependencyChain:   []string{},
	}

	// Use poker.DetectChangedResource logic but only collect listeners
	processed := &poker.Processed{
		ProcessedResources: []string{},
		Listeners:          []string{},
		Depends:            []string{},
	}

	// Simulate detection without actual pokes
	a.detectChangedResourcesNoPoke(
		ctx,
		req.ResourceType,
		req.ResourceName,
		req.Project,
		req.Version,
		processed,
		false, // managed
	)

	result.AffectedListeners = processed.Listeners
	result.DependencyChain = processed.Depends
	result.DurationMS = time.Since(startTime).Milliseconds()

	return result, nil
}

// detectChangedResourcesNoPoke simulates poker.DetectChangedResource without pokes
func (a *Analyzer) detectChangedResourcesNoPoke(
	ctx context.Context,
	gType models.GType,
	resourceName string,
	project string,
	version string,
	processed *poker.Processed,
	managed bool,
) {
	pathWithGtype := gType.String() + "===" + resourceName
	if gType != models.Listener {
		processed.Depends = append(processed.Depends, pathWithGtype)
	}

	if helper.Contains(processed.ProcessedResources, pathWithGtype) {
		return
	}

	processed.ProcessedResources = append(processed.ProcessedResources, pathWithGtype)

	if gType == models.Listener {
		if !helper.Contains(processed.Listeners, resourceName) {
			processed.Listeners = append(processed.Listeners, resourceName)
		}
	} else {
		// Process downstream resources
		dfm := downstreamfilters.DownstreamFilter{
			Name:    resourceName,
			Project: project,
			Version: version,
		}
		filterResults := gType.DownstreamFilters(dfm)

		for _, filterResult := range filterResults {
			rGeneral, err := resources.GetGenerals(ctx, a.dbContext, filterResult.Collection, filterResult.Filter)
			if err != nil {
				a.logger.Debug(err)
				continue
			}

			for _, general := range rGeneral {
				a.detectChangedResourcesNoPoke(ctx, general.GType, general.Name, project, version, processed, general.Managed)
			}
		}
	}
}
