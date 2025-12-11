package bridge

import (
	"google.golang.org/grpc"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type AppHandler struct {
	Context   *db.AppContext
	GRPCConn  *grpc.ClientConn
	BSnapshot bridge.SnapshotServiceClient
	Poke      bridge.PokeServiceClient
	Logger    *logger.Logger
}

func NewBridgeHandler(appCtx *db.AppContext) *AppHandler {
	conn, err := bridge.NewGRPCClient(appCtx)
	if err != nil {
		logger.Fatalf("did not connect: %v", err)
	}

	return &AppHandler{
		Context:   appCtx,
		GRPCConn:  conn,
		BSnapshot: bridge.NewSnapshotServiceClient(conn),
		Poke:      bridge.NewPokeServiceClient(conn),
		Logger:    logger.NewLogger("controller/bridge"),
	}
}
