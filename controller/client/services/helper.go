package services

import (
	"fmt"

	pb "github.com/CloudNativeWorks/elchi-proto/client"
)


func NewCommandWithPayload(commandID string, cmdType pb.CommandType, subType pb.SubCommandType, identity *pb.Identity, payload any) (*pb.Command, error) {
	command := &pb.Command{
		Identity:  identity,
		CommandId: commandID,
		Type:      cmdType,
		SubType:   subType,
	}

	switch p := payload.(type) {
	case *pb.Command_Deploy:
		command.Payload = p
	case *pb.Command_Service:
		command.Payload = p
	case *pb.Command_UpdateBootstrap:
		command.Payload = p
	case *pb.Command_Undeploy:
		command.Payload = p
	case *pb.Command_EnvoyAdmin:
		command.Payload = p
	case *pb.Command_GeneralLog:
		command.Payload = p
	case *pb.Command_ClientStats:
		command.Payload = p
	case *pb.Command_Network:
		command.Payload = p
	case *pb.Command_Frr:
		command.Payload = p
	default:
		return nil, fmt.Errorf("unsupported payload type: %T", payload)
	}

	return command, nil
}
