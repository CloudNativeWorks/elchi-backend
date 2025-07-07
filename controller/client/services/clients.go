package services

import (
	"context"
	"fmt"

	"sync"

	"github.com/CloudNativeWorks/elchi-backend/controller/client/client"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
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
}

// ClientWithServiceIPs, bir client ve ona bağlı servis IP'lerini temsil eder.
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
		},
		"$setOnInsert": bson.M{
			"_id":      primitive.NewObjectID(),
			"projects": []string{},
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

	// Settings collection'dan token'ı al
	var settings models.Settings
	err := s.Context.Client.Collection("settings").FindOne(context.Background(), bson.M{}).Decode(&settings)
	if err != nil {
		return nil, "", fmt.Errorf("settings token could not be retrieved: %v", err)
	}

	// Token kontrolü
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

	sessionToken := uuid.New().String()
	clientInfo := client.NewClientInfo(req, sessionToken)
	clientInfo.AccessTokens = req.GetToken()

	s.clients[req.GetClientId()] = clientInfo
	s.logger.Infof("Client registered: %s (Session Token: %s)", req.GetClientId(), sessionToken)

	// Upsert to DB
	err = s.UpsertClientToDB(context.Background(), clientInfo)
	if err != nil {
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

// getAllClientsFromDB, tüm client'ları döner.
func (s *ClientService) getAllClientsFromDB(ctx context.Context) ([]*client.ClientInfo, error) {
	cursor, err := s.Context.Client.Collection("clients").Find(ctx, bson.M{})
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

// getAllServiceIPsMap, services collection'ındaki tüm client_id -> ip eşleşmelerini map olarak döner.
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

// GetAllClients, tüm client'ları ve ilişkili servis IP'lerini döner.
func (s *ClientService) GetAllClientsWithServiceIPs() ([]*ClientWithServiceIPs, error) {
	ctx := context.Background()
	s.clientsMux.RLock()
	defer s.clientsMux.RUnlock()

	clients, err := s.getAllClientsFromDB(ctx)
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

	// Cancel previous context if exists
	if client.CancelFunc != nil {
		client.CancelFunc()
	}

	// Create new context for this client
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
