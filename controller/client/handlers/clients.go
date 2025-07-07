package handlers

import (
	"context"
	"fmt"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (h *Client) ListClients(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	if requestDetails.WithServiceIPs == "true" {
		clients, err := h.Service.GetAllClientsWithServiceIPs()
		if err != nil {
			return nil, err
		}
		return clients, nil
	}
	clients, err := h.Service.GetAllClients()
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func (h *Client) GetClient(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	fmt.Println(requestDetails.ClientID)
	client, err := h.Service.GetClientByClientID(ctx, requestDetails.ClientID)
	if err != nil {
		return nil, err
	}
	return client, nil
}