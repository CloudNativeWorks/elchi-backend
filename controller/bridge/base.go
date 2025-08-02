package bridge

import (
	"google.golang.org/grpc"

	"github.com/CloudNativeWorks/elchi-backend/pkg/bridge"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type AppHandler struct {
	Context       *db.AppContext
	GRPCConn      *grpc.ClientConn
	BSnapshot     bridge.SnapshotServiceClient
	Poke          bridge.PokeServiceClient
	Logger        *logger.Logger
}

func NewBridgeHandler(appCtx *db.AppContext) *AppHandler {
	conn, err := bridge.NewGRPCClient(appCtx)
	if err != nil {
		logger.Fatalf("did not connect: %v", err)
	}

	return &AppHandler{
		Context:       appCtx,
		GRPCConn:      conn,
		BSnapshot:     bridge.NewSnapshotServiceClient(conn),
		Poke:          bridge.NewPokeServiceClient(conn),
		Logger:        logger.NewLogger("controller/bridge"),
	}
}

/*
func checkHealth(conn *grpc.ClientConn) error {
	healthClient := grpc_health_v1.NewHealthClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	startTime := time.Now()
	for {
		resp, err := healthClient.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: ""})
		if err == nil && resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING {
			fmt.Printf("Health check failed: %v. Retrying...\n", err)
			return nil
		}

		fmt.Printf("Health check failed: %v. Retrying...\n", err)
		if time.Since(startTime) >= 30*time.Second {
			break
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("Health check failed after retries within 30 seconds")
} */

func (h *AppHandler) Close() {
	if h.GRPCConn != nil {
		h.GRPCConn.Close()
	}
}
