package common

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/mongo/options"
	"golang.org/x/exp/slices"

	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models/downstreamfilters"
)

func IsDeletable(ctx context.Context, appCtx *db.AppContext, gtype models.GType, dfm downstreamfilters.DownstreamFilter) []string {
	downstreamFilters := gtype.DownstreamFilters(dfm)
	var deletableNames []string

	// Check normal resource dependencies
	for _, filter := range downstreamFilters {
		collection := appCtx.Client.Collection(filter.Collection)
		cursor, err := collection.Find(ctx, filter.Filter, options.Find())
		if err != nil {
			appCtx.Logger.Errorf("Error finding documents: %v", err)
			continue
		}
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var result struct {
				General struct {
					Name  string `bson:"name"`
					GType string `bson:"gtype"`
				} `bson:"general"`
			}

			if err := cursor.Decode(&result); err != nil {
				appCtx.Logger.Errorf("Error decoding document: %v", err)
				continue
			}
			targetGtype := models.GType(result.General.GType)
			combined := fmt.Sprintf("Used in %s: %s", targetGtype.PrettyName(), result.General.Name)
			deletableNames = append(deletableNames, combined)
		}
		if err := cursor.Err(); err != nil {
			appCtx.Logger.Errorf("Cursor error: %v", err)
		}
	}

	// Check template usage using GType-specific template filters
	templateUsage := checkTemplateUsageForGType(ctx, appCtx, gtype, dfm.Name, dfm.Project, dfm.Version)
	deletableNames = append(deletableNames, templateUsage...)

	return deletableNames
}

// checkTemplateUsageForGType finds templates that reference the given resource using GType-specific logic
func checkTemplateUsageForGType(ctx context.Context, appCtx *db.AppContext, gtype models.GType, name, project, version string) []string {
	var templateNames []string
	collection := appCtx.Client.Collection("resource_templates")

	// Get template filters specific to this GType
	templateFilters := gtype.TemplateDownstreamFilters(name, project, version)

	for _, templateFilter := range templateFilters {
		cursor, err := collection.Find(ctx, templateFilter.Filter)
		if err != nil {
			appCtx.Logger.Errorf("Error finding template references for %s: %v", gtype.PrettyName(), err)
			continue
		}
		defer cursor.Close(ctx)

		for cursor.Next(ctx) {
			var result struct {
				Name    string `bson:"name"`
				GType   string `bson:"gtype"`
				Version string `bson:"version"`
			}

			if err := cursor.Decode(&result); err != nil {
				appCtx.Logger.Errorf("Error decoding template document: %v", err)
				continue
			}

			// Parse template gtype to get pretty name
			templateGType := models.GType(result.GType)
			templateInfo := fmt.Sprintf("Used in %s Template: %s (version: %s)", templateGType.PrettyName(), result.Name, result.Version)
			if !slices.Contains(templateNames, templateInfo) {
				templateNames = append(templateNames, templateInfo)
			}
		}

		if err := cursor.Err(); err != nil {
			appCtx.Logger.Errorf("Template cursor error: %v", err)
		}
	}

	return templateNames
}

func IsDefaultResource(ctx context.Context, appCtx *db.AppContext, name string, collection string, project string) (bool, error) {
	defaultResourceNames := map[string][]string{
		"users":      {"admin"},
		"groups":     {"default"},
		"projects":   {"default"},
		"extensions": {"elchi-control-plane-hpo", "elchi-control-plane-otel"},
		"tls":        {"elchi-control-plane-tls"},
		"clusters":   {"elchi-control-plane"},
		"filters":    {"default-router"},
	}

	if names, exists := defaultResourceNames[collection]; exists {
		if slices.Contains(names, name) {
			return true, nil
		}
	}

	return false, nil
}
