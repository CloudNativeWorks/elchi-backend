package bridge

import (
	"github.com/CloudNativeWorks/elchi-backend/control-plane/server/snapshot"
	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type BaseServiceServer struct {
	context *snapshot.Context
}

// ResourceServiceServer service.
type ResourceServiceServer struct {
	bridge.UnimplementedResourceServiceServer
	*BaseServiceServer
}

func NewResourceServiceServer(context *snapshot.Context) *ResourceServiceServer {
	return &ResourceServiceServer{
		BaseServiceServer: &BaseServiceServer{context: context},
	}
}

// SnapshotServiceServer service.
type SnapshotServiceServer struct {
	bridge.UnimplementedSnapshotServiceServer
	*BaseServiceServer
}

func NewSnapshotServiceServer(context *snapshot.Context) *SnapshotServiceServer {
	return &SnapshotServiceServer{
		BaseServiceServer: &BaseServiceServer{context: context},
	}
}

// PokeServiceServer service.
type PokeServiceServer struct {
	bridge.UnimplementedPokeServiceServer
	*BaseServiceServer
	AppContext *db.AppContext
	Logger     *logger.Logger
}

func NewPokeServiceServer(context *snapshot.Context, db *db.AppContext) *PokeServiceServer {
	return &PokeServiceServer{
		BaseServiceServer: &BaseServiceServer{context: context},
		AppContext:        db,
		Logger:            logger.NewLogger("control-plane/pokeServer"),
	}
}

