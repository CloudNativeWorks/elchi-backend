package services

import (
	"context"
	"errors"
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

	// Callback for client disconnect events
	onClientDisconnected func(clientID string)
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

// SetClientDisconnectedCallback sets the callback for client disconnect events
func (s *ClientService) SetClientDisconnectedCallback(callback func(clientID string)) {
	s.onClientDisconnected = callback
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
		return "", fmt.Errorf("default project not found: %w", err)
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

	if errors.Is(err, mongo.ErrNoDocuments) {
		// Client doesn't exist, registration allowed
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to check existing client: %w", err)
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

	if errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("no cloud configurations found for this project")
	}

	if err != nil {
		return fmt.Errorf("failed to check cloud configurations: %w", err)
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
			"client_id":         clientInfo.ClientID,
			"version":           clientInfo.Version,
			"hostname":          clientInfo.Hostname,
			"name":              clientInfo.Name,
			"os":                clientInfo.OS,
			"arch":              clientInfo.Arch,
			"kernel":            clientInfo.Kernel,
			"connected":         clientInfo.Connected,
			"last_seen":         clientInfo.LastSeen,
			"session_token":     clientInfo.SessionToken,
			"metadata":          clientInfo.Metadata,
			"access_token":      clientInfo.AccessTokens,
			"project":           clientInfo.Project,
			"bgp":               clientInfo.BGP,
			"cloud":             clientInfo.Cloud,
			"provider":          clientInfo.Provider,
			"connect_time":      clientInfo.ConnectTime,
			"connect_reason":    clientInfo.ConnectReason,
			"disconnect_time":   clientInfo.DisconnectTime,
			"disconnect_reason": clientInfo.DisconnectReason,
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
		return nil, "", fmt.Errorf("settings token could not be retrieved: %w", err)
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
	return s.GetAllClientsWithFilter(bson.M{})
}

// GetAllClientsWithFilter gets clients with custom filter
func (s *ClientService) GetAllClientsWithFilter(filter bson.M) ([]*client.ClientInfo, error) {
	s.logger.Debugf("🔍 DEBUG: GetAllClientsWithFilter started with filter: %+v", filter)

	cursor, err := s.Context.Client.Collection("clients").Find(context.Background(), filter)
	if err != nil {
		s.logger.Errorf("🔍 DEBUG: MongoDB query failed: %v", err)
		return nil, fmt.Errorf("failed to get clients: %w", err)
	}
	defer cursor.Close(context.Background())

	s.logger.Debugf("🔍 DEBUG: MongoDB query successful, processing cursor...")
	result := []*client.ClientInfo{}
	clientCount := 0
	for cursor.Next(context.Background()) {
		clientCount++
		var client client.ClientInfo
		if err := cursor.Decode(&client); err != nil {
			s.logger.Errorf("Failed to decode client: %v", err)
			continue
		}

		s.logger.Debugf("🔍 DEBUG: Processing client %d: %s (connected=%t)", clientCount, client.ClientID, client.Connected)

		result = append(result, &client)
	}

	s.logger.Debugf("🔍 DEBUG: Processed %d clients, returning %d results", clientCount, len(result))
	return result, nil
}

// GetConnectedClients returns only actually connected clients
func (s *ClientService) GetConnectedClients() ([]*client.ClientInfo, error) {
	return s.GetAllClientsWithFilter(bson.M{"connected": true})
}

// GetDisconnectedClients returns only disconnected clients
func (s *ClientService) GetDisconnectedClients() ([]*client.ClientInfo, error) {
	return s.GetAllClientsWithFilter(bson.M{"connected": false})
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
		client.ConnectTime = time.Now()
		client.ConnectReason = "session_validation"

		// Update database to reflect connected status
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			filter := bson.M{"client_id": clientID}
			update := bson.M{"$set": bson.M{
				"connected":      true,
				"last_seen":      client.LastSeen,
				"connect_time":   client.ConnectTime,
				"connect_reason": client.ConnectReason,
			}}

			_, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
			if err != nil {
				s.logger.Errorf("Failed to update client connected status in DB (ValidateSession): %v", err)
			} else {
				s.logger.Debugf("Client connected status updated in DB (ValidateSession): %s", clientID)
			}
		}()
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
	client.ConnectTime = time.Now()
	client.ConnectReason = "command_stream_connected"

	if !client.IsConnected() {
		return fmt.Errorf("failed to establish connection")
	}

	s.logger.Debugf("Client stream updated and connection established: %s", clientID)

	// Update database to reflect connected status
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		filter := bson.M{"client_id": clientID}
		update := bson.M{"$set": bson.M{
			"connected":      true,
			"last_seen":      client.LastSeen,
			"connect_time":   client.ConnectTime,
			"connect_reason": client.ConnectReason,
		}}

		_, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
		if err != nil {
			s.logger.Errorf("Failed to update client connected status in DB: %v", err)
		} else {
			s.logger.Infof("✅ Client connected status updated in DB: %s (connect_time: %v, reason: %s)",
				clientID, client.ConnectTime.Format("15:04:05"), client.ConnectReason)
		}
	}()

	return nil
}

// MarkClientDisconnectedInDBWithReason marks client as disconnected in DB with reason tracking
func (s *ClientService) MarkClientDisconnectedInDBWithReason(ctx context.Context, clientID, reason string) error {
	// Get current client state for detailed logging
	currentClient, err := s.GetClientByClientID(ctx, clientID)
	wasConnected := false
	var lastSeen time.Time

	if err == nil && currentClient != nil {
		wasConnected = currentClient.Connected
		lastSeen = currentClient.LastSeen
	}

	// Log detailed disconnection info
	s.logger.WithFields(logger.Fields{
		"client_id":           clientID,
		"reason":              reason,
		"was_connected_in_db": wasConnected,
		"last_seen":           lastSeen,
		"last_seen_age":       time.Since(lastSeen),
		"caller_context":      "db_disconnection",
	}).Warnf("🔴 MARKING CLIENT DISCONNECTED IN DB: %s (reason: %s)", clientID, reason)

	// Check if client is locally connected
	isLocallyConnected := s.IsClientConnected(clientID)
	if isLocallyConnected {
		s.logger.WithFields(logger.Fields{
			"client_id": clientID,
			"reason":    reason,
		}).Errorf("⚠️ WARNING: Marking locally connected client as disconnected in DB! This may be a bug!")
	}

	filter := bson.M{"client_id": clientID}
	update := bson.M{
		"$set": bson.M{
			"connected":         false,
			"disconnect_time":   time.Now(),
			"disconnect_reason": reason,
		},
	}

	result, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
	if err != nil {
		s.logger.Errorf("Failed to update client %s disconnection in DB: %v", clientID, err)
		return err
	}

	s.logger.WithFields(logger.Fields{
		"client_id":         clientID,
		"reason":            reason,
		"matched_count":     result.MatchedCount,
		"modified_count":    result.ModifiedCount,
		"was_connected":     wasConnected,
		"locally_connected": isLocallyConnected,
	}).Infof("✅ Client disconnection updated in DB: %s", clientID)

	return nil
}

// DisconnectClient marks client as disconnected and removes from memory
func (s *ClientService) DisconnectClient(clientID string) {
	// CRITICAL FIX: Minimize write lock time to prevent deadlock
	// Only hold lock for memory operations, do slow operations (DB, registry, callback) outside lock

	var clientToCleanup *client.ClientInfo
	var wasConnected bool
	var callbackFunc func(string)

	// PHASE 1: Quick memory cleanup under write lock (minimize lock time)
	s.clientsMux.Lock()
	if client, exists := s.clients[clientID]; exists {
		wasConnected = client.Connected
		clientToCleanup = client

		s.logger.Infof("Disconnecting client: %s (was connected: %v)", clientID, wasConnected)

		// Clean up client resources quickly and set disconnect info
		if client.CancelFunc != nil {
			client.CancelFunc()
		}
		client.Stream = nil
		client.Connected = false
		client.UpdateLastSeen()
		client.DisconnectTime = time.Now()
		client.DisconnectReason = "explicit_disconnect"

		// Remove from in-memory map immediately to prevent restart conflicts
		delete(s.clients, clientID)
		s.logger.Debugf("Client disconnected and removed from memory: %s", clientID)

		// Copy callback reference
		callbackFunc = s.onClientDisconnected
	} else {
		s.logger.Warnf("Attempted to disconnect non-existent client: %s", clientID)
	}
	s.clientsMux.Unlock() // RELEASE WRITE LOCK IMMEDIATELY

	// PHASE 2: Slow operations outside of lock (no blocking for other operations)
	if clientToCleanup != nil {
		// Update database asynchronously to prevent blocking
		go func() {
			err := s.MarkClientDisconnectedInDBWithReason(context.Background(), clientID, "explicit_disconnect")
			if err != nil {
				s.logger.Errorf("Client disconnect DB update failed: %v", err)
			}
		}()

		// Notify registry asynchronously
		go func() {
			s.notifyRegistryClientDisconnect(clientID)
		}()

		// Call disconnect callback immediately (this might be needed for active requests)
		if callbackFunc != nil {
			s.logger.Debugf("Calling disconnect callback for client: %s", clientID)
			callbackFunc(clientID)
		} else {
			s.logger.Debugf("No disconnect callback set for client: %s", clientID)
		}
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

// IsConnectionHealthy checks if client connection is healthy for gRPC operations
func (s *ClientService) IsConnectionHealthy(clientID string) bool {
	// Use timeout to prevent hanging on mutex
	done := make(chan bool, 1)
	go func() {
		s.clientsMux.RLock()
		defer s.clientsMux.RUnlock()

		client, exists := s.clients[clientID]

		if !exists {
			s.logger.Debugf("🔍 DEBUG: Health check for client %s - FAILED: Client not found", clientID)
			done <- false
			return
		}

		if !client.Connected {
			s.logger.Debugf("🔍 DEBUG: Health check for client %s - FAILED: Client not connected", clientID)
			done <- false
			return
		}

		// Check if stream is available
		if client.Stream == nil {
			s.logger.Debugf("🔍 DEBUG: Health check for client %s - FAILED: Stream is nil", clientID)
			done <- false
			return
		}

		// Check if context is cancelled
		if client.Context != nil {
			select {
			case <-client.Context.Done():
				s.logger.Debugf("🔍 DEBUG: Health check for client %s - FAILED: Context cancelled", clientID)
				done <- false
				return
			default:
				// Context still active
			}
		}

		// Check last seen time (if too old, consider unhealthy)
		// Stale threshold: 5 minutes (aligned with ping interval and sync mechanisms)
		lastSeenAge := time.Since(client.LastSeen)
		if lastSeenAge > 5*time.Minute {
			s.logger.Debugf("🔍 DEBUG: Health check for client %s - FAILED: Last seen too old (%v > 5m)",
				clientID, lastSeenAge)
			done <- false
			return
		}

		s.logger.Debugf("🔍 DEBUG: Health check for client %s - SUCCESS: All checks passed (last seen: %v ago)",
			clientID, lastSeenAge)
		done <- true
	}()

	// Wait for result with timeout
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		s.logger.Errorf("🔍 DEBUG: Health check for client %s - TIMEOUT: Mutex acquisition timed out", clientID)
		return false
	}
}

// CleanupUnhealthyConnections removes unhealthy connections to prevent stream corruption
func (s *ClientService) CleanupUnhealthyConnections() {
	// First, collect clients to check (without holding mutex)
	var clientsToCheck []string

	s.clientsMux.RLock()
	for clientID := range s.clients {
		clientsToCheck = append(clientsToCheck, clientID)
	}
	s.clientsMux.RUnlock()

	// Now check each client individually
	for _, clientID := range clientsToCheck {
		s.clientsMux.RLock()
		client, exists := s.clients[clientID]
		if !exists {
			s.clientsMux.RUnlock()
			continue
		}

		// Skip recently connected clients (grace period of 2 minutes - allows 1 missed ping)
		timeSinceLastSeen := time.Since(client.LastSeen)
		s.clientsMux.RUnlock()

		if timeSinceLastSeen < 2*time.Minute {
			s.logger.Debugf("Skipping cleanup for recently active client %s (last seen: %v ago)", clientID, timeSinceLastSeen)
			continue
		}

		// Only cleanup truly stale connections
		if !s.IsConnectionHealthy(clientID) {
			s.logger.Warnf("Client %s unhealthy, disconnecting (inactive for %v)", clientID, timeSinceLastSeen)

			// DisconnectClient() handles both memory removal and DB update atomically
			s.DisconnectClient(clientID)
		} else {
			s.logger.Debugf("Client %s passed health check (inactive for %v)", clientID, timeSinceLastSeen)

			// Update DB to reflect connected status when health check passes
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				filter := bson.M{"client_id": id}
				update := bson.M{"$set": bson.M{"connected": true}}

				_, err := s.Context.Client.Collection("clients").UpdateOne(ctx, filter, update)
				if err != nil {
					s.logger.Errorf("Failed to update client connected status in DB (health check): %v", err)
				} else {
					s.logger.Debugf("Client connected status updated in DB (health check): %s", id)
				}
			}(clientID)
		}
	}
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

// AddClientToMemoryFromPing adds a client from DB to in-memory map during ping recovery
// This is used when a client pings but is not found in memory (e.g., after cleanup or controller restart)
func (s *ClientService) AddClientToMemoryFromPing(clientInfo *client.ClientInfo) {
	s.clientsMux.Lock()
	defer s.clientsMux.Unlock()

	// Stream will be nil until client reconnects via command stream
	clientInfo.Stream = nil
	clientInfo.Context = nil
	clientInfo.CancelFunc = nil

	// Mark as connected (since they're pinging)
	clientInfo.Connected = true
	clientInfo.LastSeen = time.Now()

	s.clients[clientInfo.ClientID] = clientInfo

	s.logger.Infof("✅ Added client %s to memory from ping (recovery)", clientInfo.ClientID)
}

// HasRegistryClient checks if registry client is set
func (s *ClientService) HasRegistryClient() bool {
	return s.registryClient != nil
}

// NotifyRegistryClientConnect notifies registry about client connection (public wrapper)
func (s *ClientService) NotifyRegistryClientConnect(clientID string) {
	s.notifyRegistryClientConnect(clientID)
}
