package handlers

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

func (h *Client) ListClients(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	if requestDetails.WithServiceIPs == "true" {
		clients, err := h.Service.GetAllClientsWithServiceIPs(requestDetails.Project)
		if err != nil {
			return nil, err
		}
		return clients, nil
	}

	// Owner/Admin see every client; Editor/Viewer are scoped to the active
	// project (the clients collection carries a "project" field). Without
	// this, read-only roles would list clients across all projects, unlike
	// the service list which is permission-filtered.
	if !requestDetails.User.IsOwner && requestDetails.User.Role != models.RoleAdmin {
		clients, err := h.Service.GetAllClientsWithFilter(bson.M{"project": requestDetails.Project})
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
	client, err := h.Service.GetClientByClientID(ctx, requestDetails.ClientID)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (h *Client) DeleteClient(ctx context.Context, _ models.OperationClass, requestDetails models.RequestDetails) (any, error) {
	clientID := requestDetails.ClientID
	if clientID == "" {
		return nil, fmt.Errorf("client_id is required")
	}

	// Check if client exists
	client, err := h.Service.GetClientByClientID(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("client not found: %w", err)
	}

	// Check if client is being used by any services
	hasServices, err := h.Service.CheckClientServices(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to check client services: %w", err)
	}

	if hasServices {
		return nil, fmt.Errorf("client cannot be deleted: it is being used by one or more services")
	}

	// Delete client from database
	err = h.Service.DeleteClientFromDB(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete client: %w", err)
	}

	return map[string]string{
		"message":   fmt.Sprintf("Client %s deleted successfully", client.Name),
		"client_id": clientID,
	}, nil
}
