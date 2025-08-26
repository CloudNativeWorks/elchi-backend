package xds

import (
	"github.com/CloudNativeWorks/elchi-backend/controller/crud"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type AppHandler struct {
	crud.Application
	Logger *logger.Logger
}

func NewXDSHandler(context *db.AppContext) *AppHandler {
	conn, err := bridge.NewGRPCClient(context)
	if err != nil {
		logger.Fatalf("did not connect: %v", err)
	}

	PokeClient := bridge.NewPokeServiceClient(conn)
	ResourceServiceClient := bridge.NewResourceServiceClient(conn)
	return &AppHandler{
		Application: crud.Application{
			Context:         context,
			PokeService:     &PokeClient,
			ResourceService: &ResourceServiceClient,
		},
		Logger: logger.NewLogger("controller/xdsResource"),
	}
}
