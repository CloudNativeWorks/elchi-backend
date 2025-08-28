package services

import (
	"context"
	"fmt"
	"time"

	"sync"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/client"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/CloudNativeWorks/elchi-backend/pkg/registry"
	pb "github.com/CloudNativeWorks/elchi-proto/client"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ClientService manages client operations
type ClientService struct {
	Context          *db.AppContext
	clients          map[string]*client.ClientInfo
	clientsMux       sync.RWMutex
	pendingResponses map[string]chan *pb.CommandResponse
	pendingMux       sync.RWMutex
	logger           *logger.Logger
	registryClient   *registry.RegistryClient
}

type ClientWithServiceIPs struct {
	*client.ClientInfo
	ServiceIPs []string `json:"service_ips" bson:"service_ips"`
}

// NewClientService creates a new client service
func NewClientService(context *db.AppContext) *ClientService {
	return &ClientService{
		Context:          context,
		clients:          make(map[string]*client.ClientInfo),
		clientsMux:       sync.RWMutex{},
		pendingResponses: make(map[string]chan *pb.CommandResponse),
		pendingMux:       sync.RWMutex{},
		logger:           logger.NewLogger("controller/clientService"),
	}
}

// SetRegistryClient sets the registry client for notifications
func (s *ClientService) SetRegistryClient(client *registry.RegistryClient) {
	s.registryClient = client
}

// getDefaultProjectID fetches the default project ID from the database
func (s *ClientService) getDefaultProjectID(ctx context.Context) (string, error) {
	var defaultProject struct {
		ID primitive.ObjectID `bson:"_id"`
	}
	err := s.Context.Client.Collection("projects").FindOne(
		ctx,
		bson.M{"projectname": "default"},
	).Decode(&defaultProject)

	if err != nil {
		return "", fmt.Errorf("default project not found: %v", err)
	}

	return defaultProject.ID.Hex(), nil
}

// validateClientProjectRegistration checks if client is already registered for a different project
func (s *ClientService) validateClientProjectRegistration(ctx context.Context, clientID string, newProjectID string) error {
	var existingClient client.ClientInfo
	err := s.Context.Client.Collection("clients").FindOne(
		ctx,
		bson.M{"client_id": clientID},
	).Decode(&existingClient)

	if err == mongo.ErrNoDocuments {
		// Client doesn't exist, registration allowed
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check existing client: %v", err)
	}

	// Client exists, check if it's trying to register for a different project
	if existingClient.Project != "" && existingClient.Project != newProjectID {
		// Get project name for better error message
		projectName := s.getProjectName(ctx, existingClient.Project)
		return fmt.Errorf("client already registered for project '%s'. A client cannot be registered for multiple projects", projectName)
	}

	// Same project or no project set, allow registration
	return nil
}

// getProjectName fetches project name by ID
func (s *ClientService) getProjectName(ctx context.Context, projectID string) string {
	var project struct {
		ProjectName string `bson:"projectname"`
	}

	objID, err := primitive.ObjectIDFromHex(projectID)
	if err != nil {
		return projectID
	}

	err = s.Context.Client.Collection("projects").FindOne(
		ctx,
		bson.M{"_id": objID},
	).Decode(&project)

	if err != nil {
		return projectID
	}

	return project.ProjectName
}

// validateCloudKeyExists checks if cloud configuration exists in project settings
func (s *ClientService) validateCloudKeyExists(ctx context.Context, projectID string, cloudKey string) error {
	var settings struct {
		Clouds map[string]interface{} `bson:"clouds"`
	}

	err := s.Context.Client.Collection("settings").FindOne(
		ctx,
		bson.M{"project": projectID},
	).Decode(&settings)

	if err == mongo.ErrNoDocuments {
		return fmt.Errorf("no cloud configurations found for this project")
	}

	if err != nil {
		return fmt.Errorf("failed to check cloud configurations: %v", err)
	}

	if settings.Clouds == nil {
		return fmt.Errorf("no cloud configurations found for this project")
	}

	if _, exists := settings.Clouds[cloudKey]; !exists {
		return fmt.Errorf("cloud configuration '%s' not found in project settings", cloudKey)
	}

	return nil
}

// validateProvider checks if provider is valid
func (s *ClientService) validateProvider(provider string) error {
	validProviders := []string{"openstack", "aws", "gcp", "azure", "other"}
	
	if provider == "" {
		return fmt.Errorf("provider is required and cannot be empty")
	}

	for _, valid := range validProviders {
		if provider == valid {
			return nil
		}
	}

	return fmt.Errorf("invalid provider '%s'. Must be one of: openstack, aws, gcp, azure, other", provider)
}

// SetPendingResponse sets a pending response channel for command ID
func (s *ClientService) SetPendingResponse(commandID string, respChan chan *pb.CommandResponse) {
	s.pendingMux.Lock()
	defer s.pendingMux.Unlock()
	s.pendingResponses[commandID] = respChan
}

// RemovePendingResponse removes a pending response channel
func (s *ClientService) RemovePendingResponse(commandID string) {
	s.pendingMux.Lock()
	defer s.pendingMux.Unlock()
	delete(s.pendingResponses, commandID)
}

// notifyRegistryClientConnect notifies registry about client connection
func (s *ClientService) notifyRegistryClientConnect(clientID string) {
	if s.registryClient != nil {
		if err := s.registryClient.NotifyClientConnected(clientID); err != nil {
			s.logger.Errorf("Failed to notify registry about client connection %s: %v", clientID, err)
		}
	}
}

func (s *ClientService) UpsertClientToDB(ctx context.Context, clientInfo *client.ClientInfo) error {
	filter := bson.M{"client_id": clientInfo.ClientID}
	update := bson.M{
		"$set": bson.M{
			"client_id":     clientInfo.ClientID,
			"version":       clientInfo.Version,
			"hostname":      clientInfo.Hostname,
			"name":          clientInfo.Name,
			"os":            clientInfo.OS,
			"arch":          clientInfo.Arch,
			"kernel":        clientInfo.Kernel,
			"connected":     clientInfo.Connected,
			"last_seen":     clientInfo.LastSeen,
			"session_token": clientInfo.SessionToken,
			"metadata":      clientInfo.Metadata,
			"access_token":  clientInfo.AccessTokens,
			"project":       clientInfo.Project,
			"bgp":           clientInfo.BGP,
			"cloud":         clientInfo.Cloud,
		},
		"$setOnInsert": bson.M{
			"_id": primitive.NewObjectID(),
		},
	}
	opts := options.Update().SetUpsert(true)
	_, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update, opts)
	return err
}

// RegisterClient registers a new client
func (s *ClientService) RegisterClient(req *pb.RegisterRequest) (*client.ClientInfo, string, error) {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	ctx := context.Background()

	// Validate token
	var settings models.Settings
	err := s.Context.Client.Collection("settings").FindOne(ctx, bson.M{}).Decode(&settings)
	if err != nil {
		return nil, "", fmt.Errorf("settings token could not be retrieved: %v", err)
	}

	tokenValid := false
	for _, t := range settings.Tokens {
		if t.Token == req.GetToken() {
			tokenValid = true
			break
		}
	}

	if !tokenValid {
		return nil, "", fmt.Errorf("invalid token provided")
	}

	// Create client info
	sessionToken := uuid.New().String()
	clientInfo := client.NewClientInfo(req, sessionToken)
	clientInfo.AccessTokens = req.GetToken()

	// Handle project assignment
	if clientInfo.Project == "" {
		defaultProjectID, err := s.getDefaultProjectID(ctx)
		if err != nil {
			s.logger.Warnf("No default project found for client %s: %v", req.GetClientId(), err)
		} else {
			clientInfo.Project = defaultProjectID
			s.logger.Infof("Using default project for client %s: %s", req.GetClientId(), defaultProjectID)
		}
	}

	// Validate client isn't already registered for a different project
	if err := s.validateClientProjectRegistration(ctx, req.GetClientId(), clientInfo.Project); err != nil {
		return nil, "", err
	}

	// Validate provider
	if err := s.validateProvider(clientInfo.Provider); err != nil {
		return nil, "", err
	}

	// Handle cloud assignment - set to "other" if empty
	if clientInfo.Cloud == "" {
		clientInfo.Cloud = "other"
		s.logger.Infof("Using default cloud 'other' for client %s", req.GetClientId())
	}

	// Validate cloud key exists in project settings (if not "other")
	if clientInfo.Cloud != "other" {
		if err := s.validateCloudKeyExists(ctx, clientInfo.Project, clientInfo.Cloud); err != nil {
			return nil, "", err
		}
	}

	// Register client
	s.clients[req.GetClientId()] = clientInfo
	s.logger.Infof("Client registered: %s (Session Token: %s, Project: %s, Cloud: %s, Provider: %s)", req.GetClientId(), sessionToken, clientInfo.Project, clientInfo.Cloud, clientInfo.Provider)

	// Notify registry about client connection
	s.notifyRegistryClientConnect(req.GetClientId())

	// Upsert to DB
	if err := s.UpsertClientToDB(ctx, clientInfo); err != nil {
		s.logger.Errorf("Client could not be saved to DB: %v", err)
	}

	return clientInfo, sessionToken, nil
}

// UnregisterClient removes a client registration
func (s *ClientService) UnregisterClient(clientID string) error {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	if _, exists := s.clients[clientID]; !exists {
		return fmt.Errorf("client not found/live: %s", clientID)
	}

	delete(s.clients, clientID)
	s.logger.Debugf("Client unregistered: %s", clientID)

	// Notify registry about client disconnection
	s.notifyRegistryClientDisconnect(clientID)

	return nil
}

// GetClient retrieves client information by ID
func (s *ClientService) GetClient(clientID string) (*client.ClientInfo, error) {
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	client, exists := s.clients[clientID]
	if !exists {
		return nil, fmt.Errorf("client not found/live: %s", clientID)
	}
	return client, nil
}

func (s *ClientService) getAllClientsFromDB(ctx context.Context, projectID string) ([]*client.ClientInfo, error) {
	cursor, err := s.Context.Client.Collection("clients").Find(ctx, bson.M{"project": projectID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var clients []*client.ClientInfo
	for cursor.Next(ctx) {
		var c client.ClientInfo
		if err := cursor.Decode(&c); err != nil {
			return nil, err
		}
		clients = append(clients, &c)
	}
	return clients, nil
}

// GetClientByClientID, returns a single client.
func (s *ClientService) GetClientByClientID(ctx context.Context, clientID string) (*client.ClientInfo, error) {
	client := client.ClientInfo{}
	err := s.Context.Client.Collection("clients").FindOne(ctx, bson.M{"client_id": clientID}).Decode(&client)
	if err != nil {
		return nil, err
	}
	return &client, nil
}

func (s *ClientService) getAllServiceIPsMap(ctx context.Context) (map[string][]string, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$unwind", Value: "$clients"}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$clients.client_id"},
			{Key: "ips", Value: bson.D{{Key: "$addToSet", Value: "$clients.downstream_address"}}},
		}}},
	}
	cursor, err := s.Context.Client.Collection("services").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	ipMap := make(map[string][]string)
	for cursor.Next(ctx) {
		var row struct {
			ID  string   `bson:"_id"`
			IPs []string `bson:"ips"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, err
		}
		ipMap[row.ID] = row.IPs
	}
	return ipMap, nil
}

func (s *ClientService) GetAllClientsWithServiceIPs(projectID string) ([]*ClientWithServiceIPs, error) {
	ctx := context.Background()
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	clients, err := s.getAllClientsFromDB(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("clients find error: %w", err)
	}

	ipMap, err := s.getAllServiceIPsMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("services aggregate error: %w", err)
	}

	var results []*ClientWithServiceIPs
	for _, c := range clients {
		ips := ipMap[c.ClientID]
		results = append(results, &ClientWithServiceIPs{
			ClientInfo: c,
			ServiceIPs: ips,
		})
	}
	return results, nil
}

// GetAllClients returns all connected clients
func (s *ClientService) GetAllClients() ([]*client.ClientInfo, error) {
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()
	cursor, err := s.Context.Client.Collection("clients").Find(context.Background(), bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to get clients: %v", err)
	}
	defer cursor.Close(context.Background())

	result := []*client.ClientInfo{}
	for cursor.Next(context.Background()) {
		var client client.ClientInfo
		if err := cursor.Decode(&client); err != nil {
			return nil, fmt.Errorf("failed to decode client: %v", err)
		}
		result = append(result, &client)
	}
	return result, nil
}

// ValidateSession validates client session
func (s *ClientService) ValidateSession(clientID, sessionToken string) error {
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	client, exists := s.clients[clientID]
	if !exists {
		return fmt.Errorf("client not found/live: %s", clientID)
	}

	if client.SessionToken != sessionToken {
		client.Connected = false
		return fmt.Errorf("invalid session token")
	}

	if !client.IsConnected() {
		client.Connected = true
		client.UpdateLastSeen()
	}

	return nil
}

// UpdateClientStream updates client stream connection
func (s *ClientService) UpdateClientStream(clientID string, stream pb.CommandService_CommandStreamServer) error {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	client, exists := s.clients[clientID]
	if !exists {
		return fmt.Errorf("client not found: %s", clientID)
	}

	// Only cancel previous context if there's an existing active stream
	if client.CancelFunc != nil && client.Stream != nil {
		s.logger.Debugf("Canceling previous stream context for client: %s", clientID)
		client.CancelFunc()
		// Give a short grace period for cleanup
		time.Sleep(100 * time.Millisecond)
	}

	// Create new context for this client with timeout
	ctx, cancel := context.WithCancel(context.Background())
	client.Context = ctx
	client.CancelFunc = cancel

	client.Stream = stream
	client.Connected = true
	client.UpdateLastSeen()

	if !client.IsConnected() {
		return fmt.Errorf("failed to establish connection")
	}

	s.logger.Debugf("Client stream updated and connection established: %s", clientID)
	return nil
}

// MarkClientDisconnectedInDB marks client as disconnected in DB
func (s *ClientService) MarkClientDisconnectedInDB(ctx context.Context, clientID string) error {
	filter := bson.M{"client_id": clientID}
	update := bson.M{"$set": bson.M{"connected": false}}
	_, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
	return err
}

// DisconnectClient marks client as disconnected
func (s *ClientService) DisconnectClient(clientID string) {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	if client, exists := s.clients[clientID]; exists {
		s.logger.Infof("Disconnecting client: %s (was connected: %v)", clientID, client.Connected)

		if client.CancelFunc != nil {
			client.CancelFunc()
		}
		client.Stream = nil
		client.Connected = false
		client.UpdateLastSeen()

		s.logger.Debugf("Client disconnected: %s", clientID)

		err := s.MarkClientDisconnectedInDB(context.Background(), clientID)
		if err != nil {
			s.logger.Errorf("Client disconnect DB update failed: %v", err)
		}

		// Notify registry about client disconnection
		s.notifyRegistryClientDisconnect(clientID)
	} else {
		s.logger.Warnf("Attempted to disconnect non-existent client: %s", clientID)
	}
}

// notifyRegistryClientDisconnect notifies registry about client disconnection
func (s *ClientService) notifyRegistryClientDisconnect(clientID string) {
	if s.registryClient != nil {
		if err := s.registryClient.NotifyClientDisconnected(clientID); err != nil {
			s.logger.Errorf("Failed to notify registry about client disconnection %s: %v", clientID, err)
		}
	}
}

// DisconnectAllClients disconnects all clients
func (s *ClientService) DisconnectAllClients() {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	for clientID, client := range s.clients {
		if client.Stream != nil {
			client.Stream = nil
		}
		client.Connected = false
		s.logger.Debugf("Client disconnected: %s", clientID)
	}

	// Clean up all pending responses
	s.pendingMux.Lock()
	for commandID, respChan := range s.pendingResponses {
		close(respChan)
		delete(s.pendingResponses, commandID)
	}
	s.pendingMux.Unlock()
}

// IsClientConnected checks if a client is connected to this controller
func (s *ClientService) IsClientConnected(clientID string) bool {
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	if client, exists := s.clients[clientID]; exists {
		return client.IsConnected()
	}
	return false
}

// GetConnectedClientIDs returns all currently connected client IDs
func (s *ClientService) GetConnectedClientIDs() []string {
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	clientIDs := make([]string, 0, len(s.clients))
	for clientID, client := range s.clients {
		if client.IsConnected() {
			clientIDs = append(clientIDs, clientID)
		}
	}
	return clientIDs
}

// CheckClientServices checks if a client is being used by any services
func (s *ClientService) CheckClientServices(ctx context.Context, clientID string) (bool, error) {
	// Check in services collection if this client_id exists in any service's clients array
	count, err := s.Context.Client.Collection("services").CountDocuments(ctx, bson.M{
		"clients.client_id": clientID,
	})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeleteClientFromDB deletes a client from the database
func (s *ClientService) DeleteClientFromDB(ctx context.Context, clientID string) error {
	// First disconnect the client if it's connected
	if s.IsClientConnected(clientID) {
		s.DisconnectClient(clientID)
	}

	// Remove from in-memory clients map
	s.clientsMux.Lock()
	delete(s.clients, clientID)
	s.clientsMux.Unlock()

	// Delete from database
	result, err := s.Context.Client.Collection("clients").DeleteOne(ctx, bson.M{
		"client_id": clientID,
	})
	if err != nil {
		return err
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("client not found in database")
	}

	s.logger.Infof("Client %s deleted from database", clientID)
	return nil
}
