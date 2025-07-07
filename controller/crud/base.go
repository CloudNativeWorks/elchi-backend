package crud

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/controller/poker"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

type Application struct {
	Context         *db.AppContext
	PokeService     *bridge.PokeServiceClient
	ResourceService *bridge.ResourceServiceClient
}

func HandleResourceChange(ctx context.Context, resource models.ResourceClass, requestDetails models.RequestDetails, context *db.AppContext, project string, poke *bridge.PokeServiceClient) *poker.Processed {
	if requestDetails.SaveOrPublish == "publish" {
		initialProcessed := poker.Processed{Listeners: []string{}, Depends: []string{}}
		changedResources := poker.DetectChangedResource(
			ctx,
			resource.GetGeneral().GType,
			resource.GetGeneral().Version,
			requestDetails.Name,
			project,
			context,
			&initialProcessed,
			poke,
			resource.GetManaged(),
		)
		return changedResources
	}
	return nil
}
