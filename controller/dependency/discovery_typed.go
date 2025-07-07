package dependency

import (
	"context"

	"github.com/tidwall/gjson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func parseConfigDiscovery(ctx context.Context, appCtx *AppHandler, rootResult gjson.Result, activeResource Depend, version string) []Depend {
	var dependencies []Depend

	rootResult.Get("general.config_discovery").ForEach(func(_, discoveryItem gjson.Result) bool {
		gtypeStr := discoveryItem.Get("gtype").String()
		if gtypeStr == "" {
			return true
		}

		gtype := models.GTypes(gtypeStr)
		cdName := discoveryItem.Get("name").String()
		cdID, _ := appCtx.getResourceData(ctx, gtype.CollectionString(), cdName, activeResource.Project, version)
		dependencies = append(dependencies, Depend{Name: cdName, Gtype: gtype, Collection: gtype.CollectionString(), Project: activeResource.Project, ID: cdID})
		return true
	})

	return dependencies
}

func parseTypedConfig(ctx context.Context, appCtx *AppHandler, rootResult gjson.Result, activeResource Depend, version string) []Depend {
	var dependencies []Depend

	rootResult.Get("general.typed_config").ForEach(func(_, typedItem gjson.Result) bool {
		gtypeStr := typedItem.Get("gtype").String()
		if gtypeStr == "" {
			return true
		}

		gtype := models.GTypes(gtypeStr)
		tcName := typedItem.Get("name").String()
		tcID, _ := appCtx.getResourceData(ctx, gtype.CollectionString(), tcName, activeResource.Project, version)
		dependencies = append(dependencies, Depend{Name: tcName, Gtype: gtype, Collection: gtype.CollectionString(), Project: activeResource.Project, ID: tcID})
		return true
	})

	return dependencies
}
