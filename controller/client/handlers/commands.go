package handlers

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
)

func (h *Client) HandleSendCommand(ctx context.Context, op models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	var err error
	clients := op.GetClients()
	result := []any{}
	processor, exists := h.cmdFactory.GetProcessor(op.GetType())
	if !exists {
		h.logger.Errorf("Unsupported processor command type: %s", op.GetType())
		return nil, fmt.Errorf("unsupported processor command type: %s", op.GetType())
	}

	if len(clients) == 0 {
		clients, err = h.FetchClients(op)
		if err != nil {
			h.logger.Errorf("Failed to fetch clients: %v", err)
			return nil, fmt.Errorf("failed to fetch clients: %v", err)
		}
	}

	for _, client := range clients {
		processedPayload, err := processor.ValidateAndTransform(op, requestDetails, client)
		if err != nil {
			h.logger.Errorf("Command validation error: %v", err)
			return nil, fmt.Errorf("command validation error: %v", err)
		}

		response, err := h.Service.SendCommand(client.ClientID, op.GetTypeNum(), op.GetSubTypeNum(), processedPayload)
		if err != nil {
			h.logger.WithFields(logger.Fields{
				"client_id":          client.ClientID,
				"downstream_address": client.DownstreamAddress,
				"error":              err,
			}).Errorf("Command sending error")
			return nil, fmt.Errorf("command sending error: %v", err)
		}

		responser, exists := h.responser.GetResponser(op.GetType())
		if !exists {
			h.logger.Errorf("Unsupported responser command type: %s", op.GetType())
			return nil, fmt.Errorf("unsupported responser command type: %s", op.GetType())
		}

		result = append(result, responser.ValidateAndTransform(op, response))
	}

	return result, nil
}

func (s *Client) FetchClients(op models.OperationClass) ([]models.ServiceClients, error) {
	collection := s.Context.Client.Collection("services")
	filter := bson.M{
		"name":    op.GetCommandName(),
		"project": op.GetCommandProject(),
	}

	var result struct {
		Clients []models.ServiceClients `bson:"clients"`
	}

	err := collection.FindOne(context.Background(), filter).Decode(&result)
	if err != nil {
		return nil, err
	}

	return result.Clients, nil
}
