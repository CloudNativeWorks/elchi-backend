package grpc

import (
	"context"

	"github.com/CloudNativeWorks/elchi-backend/controller/forward"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
)

// ControllerServer implements ControllerService gRPC server
type ControllerServer struct {
	pb.UnimplementedControllerServiceServer
	forwardHandler *forward.ForwardHandler
}

// NewControllerServer creates a new controller server
func NewControllerServer(handler *forward.ForwardHandler) *ControllerServer {
	return &ControllerServer{
		forwardHandler: handler,
	}
}

// ForwardCommand handles forwarded commands from other controllers
func (s *ControllerServer) ForwardCommand(ctx context.Context, req *pb.ForwardCommandRequest) (*pb.ForwardCommandResponse, error) {
	return s.forwardHandler.HandleForwardCommand(ctx, req)
}

// Other methods are not implemented since this controller only handles ForwardCommand
// Registry methods are handled by the registry service itself
func (s *ControllerServer) RegisterController(ctx context.Context, req *pb.ControllerInfo) (*pb.ControllerResponse, error) {
	return &pb.ControllerResponse{Success: "false"}, nil
}

func (s *ControllerServer) GetClientLocation(ctx context.Context, req *pb.ClientLocationRequest) (*pb.ClientLocationResponse, error) {
	return &pb.ClientLocationResponse{Found: false}, nil
}

func (s *ControllerServer) SetClientLocation(ctx context.Context, req *pb.SetClientLocationRequest) (*pb.SetClientLocationResponse, error) {
	return &pb.SetClientLocationResponse{Success: "false"}, nil
}

func (s *ControllerServer) RequestClientRefresh(ctx context.Context, req *pb.ClientRefreshRequest) (*pb.ClientRefreshResponse, error) {
	return &pb.ClientRefreshResponse{Success: "false"}, nil
} 