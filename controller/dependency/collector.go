package dependency

import (
	"context"
	"fmt"

	"github.com/tidwall/gjson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
)

func GenericUpstreamCollector(ctx context.Context, appCtx *AppHandler, activeResource Depend, version string) (Node, []Depend) {
	var dependencies []Depend
	id, jsonData := appCtx.getResourceData(ctx, activeResource.Collection, activeResource.Name, activeResource.Project, version)
	rootResult := gjson.Parse(jsonData)

	node := Node{
		ID:         id,
		Name:       activeResource.Name,
		Gtype:      activeResource.Gtype,
		Collection: activeResource.Gtype.CollectionString(),
		Link:       activeResource.Gtype.URL(),
		First:      activeResource.First,
		Direction:  "upstream",
	}

	jsonPaths := getDynamicJSONPaths(activeResource.Gtype)
	for path, gtype := range jsonPaths {
		resourcePath := fmt.Sprintf("%s.%s", "resource.resource", path)
		collectDependenciesFromPath(ctx, appCtx, rootResult, resourcePath, gtype, activeResource, &dependencies, version)
	}

	dependencies = append(dependencies, parseTypedConfig(ctx, appCtx, rootResult, activeResource, version)...)
	dependencies = append(dependencies, parseConfigDiscovery(ctx, appCtx, rootResult, activeResource, version)...)

	if len(dependencies) == 0 {
		appCtx.Logger.Debugf("No dependencies found for resource: %s of type %s", activeResource.Name, activeResource.Gtype)
	}

	return node, dependencies
}

func collectDependenciesFromPath(ctx context.Context, appCtx *AppHandler, rootResult gjson.Result, path string, gtype models.GTypes, activeResource Depend, dependencies *[]Depend, version string) {
	results := rootResult.Get(path)

	if !results.Exists() {
		appCtx.Logger.Debugf("Result does not exist at path: %s", path)
		return
	}

	results.ForEach(func(_, item gjson.Result) bool {
		if item.IsArray() {
			item.ForEach(func(_, subItem gjson.Result) bool {
				processItem(ctx, appCtx, subItem, path, gtype, activeResource, dependencies, version)
				return true
			})
		} else {
			processItem(ctx, appCtx, item, path, gtype, activeResource, dependencies, version)
		}
		return true
	})
}

func processItem(ctx context.Context, appCtx *AppHandler, item gjson.Result, path string, gtype models.GTypes, activeResource Depend, dependencies *[]Depend, version string) {
	if item.IsArray() {
		item.ForEach(func(_, subItem gjson.Result) bool {
			addDependency(ctx, appCtx, subItem.String(), path, gtype, activeResource, dependencies, version)
			return true
		})
	} else {
		addDependency(ctx, appCtx, item.String(), path, gtype, activeResource, dependencies, version)
	}
}

func addDependency(ctx context.Context, appCtx *AppHandler, name, path string, gtype models.GTypes, activeResource Depend, dependencies *[]Depend, version string) {
	if name == "" {
		appCtx.Logger.Debugf("Name not found at path: %s for gtype: %s", path, gtype)
		return
	}

	itemID, _ := appCtx.getResourceData(ctx, gtype.CollectionString(), name, activeResource.Project, version)
	if itemID == "" {
		appCtx.Logger.Debugf("ID not found for %s of type %s, skipping... Path: %s", name, gtype, path)
		return
	}

	dependency := Depend{
		Name:       name,
		Gtype:      gtype,
		Collection: gtype.CollectionString(),
		Project:    activeResource.Project,
		ID:         itemID,
		Direction:  "upstream",
	}

	*dependencies = append(*dependencies, dependency)
	appCtx.Logger.Debugf("Added dependency: %s of type %s with ID: %s", dependency.Name, dependency.Gtype, dependency.ID)
}

func GenericDownstreamCollector(ctx context.Context, appCtx *AppHandler, activeResource Depend, visited map[string]bool, version string) (Node, []Depend) {
	var dependencies []Depend
	if activeResource.ID == "" {
		id, _ := appCtx.getResourceData(ctx, activeResource.Collection, activeResource.Name, activeResource.Project, version)
		activeResource.ID = id
	}

	uniqueKey := generateUniqueKey(activeResource)
	if visited[uniqueKey] {
		return Node{}, nil
	}
	visited[uniqueKey] = true

	node := Node{
		ID:         activeResource.ID,
		Name:       activeResource.Name,
		Gtype:      activeResource.Gtype,
		Collection: activeResource.Gtype.CollectionString(),
		Link:       activeResource.Gtype.URL(),
		First:      activeResource.First,
		Direction:  "downstream",
	}

	dfm := downstreamfilters.DownstreamFilter{
		Name:    activeResource.Name,
		Project: activeResource.Project,
		Version: version,
	}

	downstreamFilters := activeResource.Gtype.DownstreamFilters(dfm)
	for _, filter := range downstreamFilters {
		collectDependenciesFromFilter(ctx, appCtx, filter, activeResource, &dependencies)
	}

	for _, dep := range dependencies {
		if dep.Direction == "downstream" {
			_, downstreamDeps := GenericDownstreamCollector(ctx, appCtx, dep, visited, version)
			dependencies = append(dependencies, downstreamDeps...)
		}
	}

	return node, dependencies
}

func collectDependenciesFromFilter(ctx context.Context, appCtx *AppHandler, filter downstreamfilters.MongoFilters, activeResource Depend, dependencies *[]Depend) {
	collection := filter.Collection
	query := filter.Filter

	cursor, err := appCtx.Context.Client.Collection(collection).Find(ctx, query)
	if err != nil {
		appCtx.Logger.Debugf("Error fetching downstream dependencies: %v", err)
		return
	}

	for cursor.Next(ctx) {
		var resource models.DBResource
		if err := cursor.Decode(&resource); err != nil {
			appCtx.Logger.Debugf("Error decoding downstream resource: %v", err)
			continue
		}

		if resource.General.Name == activeResource.Name && resource.General.GType == activeResource.Gtype {
			continue
		}

		dependency := Depend{
			Name:       resource.General.Name,
			Gtype:      resource.General.GType,
			Collection: collection,
			Project:    activeResource.Project,
			ID:         resource.ID.Hex(),
			Direction:  "downstream",
			Source:     activeResource.ID,
		}

		*dependencies = append(*dependencies, dependency)
		appCtx.Logger.Debugf("Added downstream dependency: %s of type %s with ID: %s", dependency.Name, dependency.Gtype, dependency.ID)
	}
}
