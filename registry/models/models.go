package models

// ControllerInfo represents a controller instance
type ControllerInfo struct {
	ID          string `json:"controller_id" bson:"controller_id"`
	GRPCAddress string `json:"grpc_address" bson:"grpc_address"`
}

// ClientLocation represents where a client is currently connected
type ClientLocation struct {
	ClientID     string `json:"client_id" bson:"client_id"`
	ControllerID string `json:"controller_id" bson:"controller_id"`
} 