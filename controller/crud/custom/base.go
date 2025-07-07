package custom

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type AppHandler struct {
	crud.Application
	Logger *logger.Logger
}

func NewCustomHandler(context *db.AppContext) *AppHandler {
	logger := logger.NewLogger("controller/customResource")
	return &AppHandler{
		Application: crud.Application{
			Context: context,
		},
		Logger: logger,
	}
}
