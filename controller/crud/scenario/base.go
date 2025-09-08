package scenario

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/extension"
	"github.com/CloudNativeWorks/elchi-backend/controller/crud/xds"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type AppHandler struct {
	Context   *db.AppContext
	XDS       *xds.AppHandler
	Extension *extension.AppHandler
	Logger    *logger.Logger
}

func NewScenarioHandler(context *db.AppContext) *AppHandler {
	xdsHandler := xds.NewXDSHandler(context)
	extensionHandler := extension.NewExtensionHandler(context)
	return &AppHandler{
		Context:   context,
		XDS:       xdsHandler,
		Extension: extensionHandler,
		Logger:    logger.NewLogger("controller/scenario"),
	}
}
