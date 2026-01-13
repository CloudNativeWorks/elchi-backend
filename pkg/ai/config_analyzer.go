package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
)

// ConfigAnalyzerRequest represents user's analyzer request
type ConfigAnalyzerRequest struct {
	// Resource information (required)
	ResourceName string `json:"resource_name" validate:"required"`
	Collection   string `json:"collection" validate:"required"` // listeners, filters, clusters, routes, etc.
	Project      string `json:"project" validate:"required"`
	Version      string `json:"version" validate:"required"`

	// User question
	Question string `json:"question" validate:"required"`

	// Optional context
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	Depth               int  `json:"depth,omitempty"` // Dependency depth (default: 3)
}

// LogAnalyzerRequest represents user's log analysis request
type LogAnalyzerRequest struct {
	// Resource information (required)
	ResourceName string `json:"resource_name" validate:"required"`
	Collection   string `json:"collection" validate:"required"` // listeners, filters, clusters, routes, etc.
	Project      string `json:"project" validate:"required"`

	// Log data
	Logs string `json:"logs" validate:"required"`

	// User question about logs (optional - if empty, general analysis will be performed)
	Question string `json:"question,omitempty"`

	// Optional context
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	Depth               int  `json:"depth,omitempty"` // Dependency depth (default: 3)
}

// ConfigAnalysisResult represents analysis result
type ConfigAnalysisResult struct {
	ResourceConfig   models.DBResource              `json:"resource_config"`
	Dependencies     *dependency.Graph              `json:"dependencies,omitempty"`
	RelatedResources map[string][]models.DBResource `json:"related_resources"`
	Analysis         string                         `json:"analysis"`
	Suggestions      []string                       `json:"suggestions,omitempty"`
	Warnings         []string                       `json:"warnings,omitempty"`
	ProcessedAt      time.Time                      `json:"processed_at"`
	TokenUsage       TokenUsage                     `json:"token_usage"`
}

type TokenUsage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// LogAnalysisResult represents log analysis result
type LogAnalysisResult struct {
	ResourceConfig   models.DBResource              `json:"resource_config"`
	Dependencies     *dependency.Graph              `json:"dependencies,omitempty"`
	RelatedResources map[string][]models.DBResource `json:"related_resources"`
	LogSummary       string                         `json:"log_summary"`
	Analysis         string                         `json:"analysis"`
	Suggestions      []string                       `json:"suggestions,omitempty"`
	Warnings         []string                       `json:"warnings,omitempty"`
	ErrorsDetected   []string                       `json:"errors_detected,omitempty"`
	LogLineCount     int                            `json:"log_line_count"`
	ProcessedAt      time.Time                      `json:"processed_at"`
	TokenUsage       TokenUsage                     `json:"token_usage"`
}

// ConfigAnalyzer analyzes resources and their dependencies using OpenRouter
type ConfigAnalyzer struct {
	dbContext         *db.AppContext
	dependencyHandler *dependency.AppHandler
	aiClient          *OpenRouterClient // Changed from Claude to OpenRouter
	defaultModel      string            // Default model to use
	logger            *logger.Logger
	systemPrompt      string // Cached system prompt
	usageTracker      *UsageTracker
}

func NewConfigAnalyzer(dbContext *db.AppContext, aiClient *OpenRouterClient, defaultModel string, logger *logger.Logger) *ConfigAnalyzer {
	dependencyHandler := dependency.NewDependencyHandler(dbContext)
	usageTracker := NewUsageTracker(dbContext)

	analyzer := &ConfigAnalyzer{
		dbContext:         dbContext,
		dependencyHandler: dependencyHandler,
		usageTracker:      usageTracker,
		aiClient:          aiClient,
		defaultModel:      defaultModel,
		logger:            logger,
	}

	// Build and cache system prompt once
	analyzer.systemPrompt = analyzer.buildAnalysisSystemPrompt()
	logger.Debug("System prompt cached for ConfigAnalyzer")

	return analyzer
}

// recordUsage records AI usage statistics
func (ca *ConfigAnalyzer) recordUsage(ctx context.Context, record AIUsageRecord) {
	// Non-blocking usage recording
	go func() {
		if err := ca.usageTracker.RecordUsage(ctx, record); err != nil {
			ca.logger.Errorf("Failed to record AI usage: %v", err)
		}
	}()
}

// AnalyzeResourceConfig analyzes any resource and its dependencies using AI
func (ca *ConfigAnalyzer) AnalyzeResourceConfig(ctx context.Context, req ConfigAnalyzerRequest, userID string) (*ConfigAnalysisResult, error) {
	startTime := time.Now()
	// 1. Get resource from MongoDB
	resource, err := ca.getResourceFromDB(ctx, req.Collection, req.ResourceName, req.Project, req.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	result := &ConfigAnalysisResult{
		ResourceConfig:   *resource,
		RelatedResources: make(map[string][]models.DBResource),
		ProcessedAt:      time.Now(),
	}

	// 2. Discover dependencies
	if req.IncludeDependencies {
		dependencies, relatedResources, err := ca.discoverDependencies(ctx, req)
		if err != nil {
			ca.logger.Warnf("Failed to discover dependencies: %v", err)
			result.Warnings = append(result.Warnings, fmt.Sprintf("Dependency discovery failed: %v", err))
		} else {
			result.Dependencies = dependencies
			result.RelatedResources = relatedResources
		}
	}

	// 3. Analyze with AI
	analysis, suggestions, inputTokens, outputTokens, err := ca.analyzeWithAI(req, result)
	if err != nil {
		// Enhanced error handling for OpenRouter errors
		errorMessage := err.Error()

		// Check if it's an OpenRouter error and extract relevant info
		var openRouterErr *OpenRouterError
		if errors.As(err, &openRouterErr) {
			ca.logger.Errorf("OpenRouter API error - Status: %d, Model: %s, Message: %s",
				openRouterErr.StatusCode, openRouterErr.Model, openRouterErr.Message)

			// Don't expose raw response body to frontend
			errorMessage = fmt.Sprintf("AI service error (%d): %s", openRouterErr.StatusCode, openRouterErr.Message)
		}

		// Record failed usage
		ca.recordUsage(ctx, AIUsageRecord{
			Project:      req.Project,
			UserID:       userID,
			RequestType:  "analyze",
			ModelID:      ca.defaultModel,
			Provider:     ca.getProviderFromModel(ca.defaultModel),
			ResourceName: req.ResourceName,
			Collection:   req.Collection,
			Success:      false,
			ErrorMessage: err.Error(), // Log full error internally
			Duration:     time.Since(startTime).Milliseconds(),
		})
		return nil, fmt.Errorf("%s", errorMessage) // Return clean error to frontend
	}

	result.Analysis = analysis
	result.Suggestions = suggestions
	result.TokenUsage = TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		CostUSD:      calculateTokenCost(inputTokens, outputTokens, ca.defaultModel),
	}

	// Record successful usage
	ca.recordUsage(ctx, AIUsageRecord{
		Project:      req.Project,
		UserID:       userID,
		RequestType:  "analyze",
		ModelID:      ca.defaultModel,
		Provider:     ca.getProviderFromModel(ca.defaultModel),
		ResourceName: req.ResourceName,
		Collection:   req.Collection,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Success:      true,
		Duration:     time.Since(startTime).Milliseconds(),
	})

	return result, nil
}

// AnalyzeLogsWithConfig analyzes Envoy logs with resource context using AI
func (ca *ConfigAnalyzer) AnalyzeLogsWithConfig(ctx context.Context, req LogAnalyzerRequest, userID string) (*LogAnalysisResult, error) {
	startTime := time.Now()
	// 1. Validate log line count (max 500 lines)
	logLines := strings.Split(strings.TrimSpace(req.Logs), "\n")
	if len(logLines) > 500 {
		return nil, fmt.Errorf("log too large: %d lines (maximum 500 lines allowed)", len(logLines))
	}

	// 2. Get resource from MongoDB (without version - find by project and name only)
	resource, err := ca.getResourceFromDBWithoutVersion(ctx, req.Collection, req.ResourceName, req.Project)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	result := &LogAnalysisResult{
		ResourceConfig:   *resource,
		RelatedResources: make(map[string][]models.DBResource),
		LogLineCount:     len(logLines),
		ProcessedAt:      time.Now(),
	}

	// 3. Discover dependencies
	if req.IncludeDependencies {
		dependencies, relatedResources, err := ca.discoverDependenciesForLog(ctx, req, resource)
		if err != nil {
			ca.logger.Warnf("Failed to discover dependencies: %v", err)
			result.Warnings = append(result.Warnings, fmt.Sprintf("Dependency discovery failed: %v", err))
		} else {
			result.Dependencies = dependencies
			result.RelatedResources = relatedResources
		}
	}

	// 4. Analyze logs with AI
	analysis, suggestions, errorss, logSummary, inputTokens, outputTokens, err := ca.analyzeLogsWithAI(req, result)
	if err != nil {
		// Enhanced error handling for OpenRouter errors
		errorMessage := err.Error()

		// Check if it's an OpenRouter error and extract relevant info
		var openRouterErr *OpenRouterError
		if errors.As(err, &openRouterErr) {
			ca.logger.Errorf("OpenRouter API error - Status: %d, Model: %s, Message: %s",
				openRouterErr.StatusCode, openRouterErr.Model, openRouterErr.Message)

			// Don't expose raw response body to frontend
			errorMessage = fmt.Sprintf("AI service error (%d): %s", openRouterErr.StatusCode, openRouterErr.Message)
		}

		// Record failed usage
		ca.recordUsage(ctx, AIUsageRecord{
			Project:      req.Project,
			UserID:       userID,
			RequestType:  "analyze-logs",
			ModelID:      ca.defaultModel,
			Provider:     ca.getProviderFromModel(ca.defaultModel),
			ResourceName: req.ResourceName,
			Collection:   req.Collection,
			Success:      false,
			ErrorMessage: err.Error(), // Log full error internally
			Duration:     time.Since(startTime).Milliseconds(),
		})
		return nil, fmt.Errorf("%s", errorMessage) // Return clean error to frontend
	}

	result.Analysis = analysis
	result.Suggestions = suggestions
	result.ErrorsDetected = errorss
	result.LogSummary = logSummary
	result.TokenUsage = TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		CostUSD:      calculateTokenCost(inputTokens, outputTokens, ca.defaultModel),
	}

	// Record successful usage
	ca.recordUsage(ctx, AIUsageRecord{
		Project:      req.Project,
		UserID:       userID,
		RequestType:  "analyze-logs",
		ModelID:      ca.defaultModel,
		Provider:     ca.getProviderFromModel(ca.defaultModel),
		ResourceName: req.ResourceName,
		Collection:   req.Collection,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Success:      true,
		Duration:     time.Since(startTime).Milliseconds(),
	})

	return result, nil
}

// getResourceFromDB gets any resource from MongoDB
func (ca *ConfigAnalyzer) getResourceFromDB(ctx context.Context, collectionName, name, project, version string) (*models.DBResource, error) {
	collection := ca.dbContext.Client.Collection(collectionName)

	// Basic filter
	filter := bson.M{
		"general.name":    name,
		"general.project": project,
		"general.version": version,
	}

	ca.logger.Debugf("Searching for resource in %s with filter: %+v", collectionName, filter)

	var resource models.DBResource
	err := collection.FindOne(ctx, filter).Decode(&resource)
	if err != nil {
		ca.logger.Errorf("MongoDB query failed: %v", err)
		return nil, fmt.Errorf("resource not found in %s: name=%s, project=%s, version=%s, error=%w",
			collectionName, name, project, version, err)
	}

	ca.logger.Debugf("Found resource: %s/%s", collectionName, resource.General.Name)
	return &resource, nil
}

// getGTypeFromCollection extracts GType from collection name
func (ca *ConfigAnalyzer) getGTypeFromCollection(collection string) models.GType {
	switch collection {
	case "listeners":
		return models.Listener
	case "clusters":
		return models.Cluster
	case "routes":
		return models.Route
	case "endpoints":
		return models.Endpoint
	case "virtual_hosts":
		return models.VirtualHost
	case "filters":
		return models.HTTPConnectionManager // Most common for filters
	case "extensions":
		return models.FileAccessLog // Most common for extensions
	case "secrets":
		return models.GenericSecret
	case "tls":
		return models.DownstreamTLSContext
	default:
		ca.logger.Warnf("Unknown collection: %s, defaulting to Listener", collection)
		return models.Listener
	}
}

// discoverDependencies discovers resource dependencies
func (ca *ConfigAnalyzer) discoverDependencies(ctx context.Context, req ConfigAnalyzerRequest) (*dependency.Graph, map[string][]models.DBResource, error) {
	// Determine GType from collection
	gType := ca.getGTypeFromCollection(req.Collection)

	requestDetails := models.RequestDetails{
		Name:       req.ResourceName,
		Project:    req.Project,
		Version:    req.Version,
		Collection: req.Collection,
		GType:      gType,
	}

	// Build dependency graph
	dependencyGraph, err := ca.dependencyHandler.GetResourceDependencies(ctx, requestDetails)
	if err != nil {
		return nil, nil, err
	}

	// Collect related resources
	relatedResources := make(map[string][]models.DBResource)

	// Fetch resources for each dependency node
	for _, node := range dependencyGraph.Nodes {
		// Get specific resource for each dependency node
		resource, err := ca.getSpecificResource(ctx, node.Data.Category, node.Data.Label, req.Project, req.Version)
		if err != nil {
			ca.logger.Warnf("Failed to get resource %s/%s: %v", node.Data.Category, node.Data.Label, err)
			continue
		}

		// Group by collection
		if relatedResources[node.Data.Category] == nil {
			relatedResources[node.Data.Category] = []models.DBResource{}
		}
		relatedResources[node.Data.Category] = append(relatedResources[node.Data.Category], *resource)
	}

	return dependencyGraph, relatedResources, nil
}

// getSpecificResource gets specific resource from MongoDB
func (ca *ConfigAnalyzer) getSpecificResource(ctx context.Context, collection, name, project, version string) (*models.DBResource, error) {
	mongoCollection := ca.dbContext.Client.Collection(collection)

	filter := bson.M{
		"general.name":    name,
		"general.project": project,
		"general.version": version,
	}

	ca.logger.Debugf("Fetching resource from %s: name=%s, project=%s, version=%s", collection, name, project, version)

	var resource models.DBResource
	err := mongoCollection.FindOne(ctx, filter).Decode(&resource)
	if err != nil {
		return nil, fmt.Errorf("resource not found in %s: name=%s, project=%s, version=%s, error=%w",
			collection, name, project, version, err)
	}

	ca.logger.Debugf("Found resource: %s/%s", collection, resource.General.Name)
	return &resource, nil
}

// getResourceFromDBWithoutVersion gets resource from MongoDB without version requirement (for log analysis)
func (ca *ConfigAnalyzer) getResourceFromDBWithoutVersion(ctx context.Context, collectionName, name, project string) (*models.DBResource, error) {
	collection := ca.dbContext.Client.Collection(collectionName)

	// Basic filter without version
	filter := bson.M{
		"general.name":    name,
		"general.project": project,
	}

	ca.logger.Debugf("Searching for resource in %s without version filter: name=%s, project=%s", collectionName, name, project)

	var resource models.DBResource
	err := collection.FindOne(ctx, filter).Decode(&resource)
	if err != nil {
		ca.logger.Errorf("MongoDB query failed: %v", err)
		return nil, fmt.Errorf("resource not found in %s: name=%s, project=%s, error=%w",
			collectionName, name, project, err)
	}

	ca.logger.Debugf("Found resource: %s/%s (version: %s)", collectionName, resource.General.Name, resource.General.Version)
	return &resource, nil
}

// discoverDependenciesForLog discovers resource dependencies for log analysis
func (ca *ConfigAnalyzer) discoverDependenciesForLog(ctx context.Context, req LogAnalyzerRequest, resource *models.DBResource) (*dependency.Graph, map[string][]models.DBResource, error) {
	// Determine GType from collection
	gType := ca.getGTypeFromCollection(req.Collection)

	requestDetails := models.RequestDetails{
		Name:       req.ResourceName,
		Project:    req.Project,
		Version:    resource.General.Version, // Use version from the found resource
		Collection: req.Collection,
		GType:      gType,
	}

	// Build dependency graph
	dependencyGraph, err := ca.dependencyHandler.GetResourceDependencies(ctx, requestDetails)
	if err != nil {
		return nil, nil, err
	}

	// Collect related resources
	relatedResources := make(map[string][]models.DBResource)

	// Fetch resources for each dependency node
	for _, node := range dependencyGraph.Nodes {
		// Get specific resource for each dependency node
		depResource, err := ca.getSpecificResource(ctx, node.Data.Category, node.Data.Label, req.Project, resource.General.Version)
		if err != nil {
			ca.logger.Warnf("Failed to get resource %s/%s: %v", node.Data.Category, node.Data.Label, err)
			continue
		}

		// Group by collection
		if relatedResources[node.Data.Category] == nil {
			relatedResources[node.Data.Category] = []models.DBResource{}
		}
		relatedResources[node.Data.Category] = append(relatedResources[node.Data.Category], *depResource)
	}

	return dependencyGraph, relatedResources, nil
}

// analyzeWithAI performs AI configuration analysis
func (ca *ConfigAnalyzer) analyzeWithAI(req ConfigAnalyzerRequest, result *ConfigAnalysisResult) (string, []string, int, int, error) {
	// Create user prompt (system prompt already cached)
	userPrompt := ca.buildAnalysisUserPrompt(req, result)

	// Call OpenRouter API
	openRouterReq := OpenRouterRequest{
		Model:       ca.defaultModel,
		MaxTokens:   4000,
		Temperature: 0.1,
		Messages: []OpenRouterMessage{
			{
				Role:    "system",
				Content: ca.systemPrompt,
			},
			{
				Role:    "user",
				Content: userPrompt,
			},
		},
	}

	ctx := context.Background()
	response, err := ca.aiClient.GetCompletion(ctx, openRouterReq)
	if err != nil {
		return "", nil, 0, 0, err
	}

	if len(response.Choices) == 0 {
		return "", nil, 0, 0, fmt.Errorf("empty response choices")
	}

	// Parse response
	analysis, suggestions := ca.parseAnalysisResponse(response.Choices[0].Message.Content)

	return analysis, suggestions, response.Usage.PromptTokens, response.Usage.CompletionTokens, nil
}

// analyzeLogsWithAI analyzes Envoy logs with configuration context using AI
func (ca *ConfigAnalyzer) analyzeLogsWithAI(req LogAnalyzerRequest, result *LogAnalysisResult) (string, []string, []string, string, int, int, error) {
	// Build user prompt for log analysis (system prompt is already cached)
	userPrompt := ca.buildLogAnalysisUserPrompt(req, result)

	// Call OpenRouter API with log-specific system prompt
	openRouterReq := OpenRouterRequest{
		Model:       ca.defaultModel,
		MaxTokens:   4000,
		Temperature: 0.1,
		Messages: []OpenRouterMessage{
			{
				Role:    "system",
				Content: ca.buildLogAnalysisSystemPrompt(), // Use log-specific system prompt
			},
			{
				Role:    "user",
				Content: userPrompt,
			},
		},
	}

	ctx := context.Background()
	response, err := ca.aiClient.GetCompletion(ctx, openRouterReq)
	if err != nil {
		return "", nil, nil, "", 0, 0, err
	}

	if len(response.Choices) == 0 {
		return "", nil, nil, "", 0, 0, fmt.Errorf("empty response choices")
	}

	// Parse response
	analysis, suggestions, errors, logSummary := ca.parseLogAnalysisResponse(response.Choices[0].Message.Content)

	return analysis, suggestions, errors, logSummary, response.Usage.PromptTokens, response.Usage.CompletionTokens, nil
}

// buildAnalysisSystemPrompt creates system prompt for AI analysis
func (ca *ConfigAnalyzer) buildAnalysisSystemPrompt() string {
	return `You are a helpful AI assistant for the Elchi Envoy proxy management system. You can answer general questions about networking, domains, proxies, and other technical topics. When users specifically ask about Envoy configurations, you will provide detailed analysis and UI-based instructions.

## ELCHI SYSTEM ARCHITECTURE:

### **🏗️ 3 Main Components:**

#### **1. Registry Process** (Port: 9090)
- **Purpose:** Service discovery and routing service
- **Features:**
  - Controller registration and address sharing
  - Client location tracking (which controller they're connected to)
  - Control-plane routing (version-based routing)
  - External Processing (integration with Envoy ext_proc protocol)
  - In-memory data storage (high performance)
  - gRPC API service

#### **2. Controller Process** (HTTP Port: Configurable)
- **Purpose:** Main management and API service  
- **Features:**
  - REST API endpoints (where AI APIs are hosted)
  - Client management and command dispatching
  - XDS resource management (clusters, listeners, routes, endpoints)
  - User and authorization management
  - MongoDB database integration
  - JWT-based authentication

#### **3. Control-Plane Process** (gRPC Port: 18000)
- **Purpose:** Envoy xDS management service
- **Features:**
  - Envoy ADS (Aggregated Discovery Service) service
  - VHDS (Virtual Host Discovery Service) service
  - Snapshot management and cache system
  - Bridge services (snapshot, resource, poke)
  - Automatic registration with registry

### **🔄 System Communication Flow:**
1. **Envoy Instances** → Connect to **Control-Plane** for xDS configs
2. **Control-Plane** → Registers with **Registry** for discovery
3. **Controller** → Manages resources in **MongoDB**
4. **Controller** → Communicates with **Registry** for client routing
5. **Frontend UI** → Calls **Controller** REST APIs
6. **AI Analysis** → Runs on **Controller** process

### **📊 MongoDB Collections:**
- **listeners, clusters, routes, endpoints, virtual_hosts** → Envoy resources
- **filters, extensions, secrets, tls** → Additional configurations  
- **users, groups, projects, settings** → User management
- **envoys** → Connected Envoy instances and status

## YOUR ROLE:
You are an expert assistant for the Elchi Envoy proxy management system. You can help users with:

1. **GENERAL QUESTIONS**: Answer any question about networking, domains, proxy concepts, or technical topics
2. **CONFIG ANALYSIS**: When specifically asked, analyze Envoy resources and their dependencies
3. **UI GUIDANCE**: Provide step-by-step UI instructions when users need help with the interface
4. **PROBLEM SOLVING**: Help troubleshoot issues and provide practical solutions
5. **SYSTEM ARCHITECTURE**: Explain how the 3-component Elchi system works

**IMPORTANT**: Only focus on Envoy configuration when the user specifically asks about Envoy configs, listeners, clusters, routes, etc. For general questions about domains, networking, or other topics, provide general helpful answers.

**SECURITY NOTICE**: For security reasons, TLS certificate resources (TlsCertificate, CertificateValidationContext, GenericSecret, TlsSessionTicketKeys) are excluded from the analysis data. If you notice missing certificate information in the configuration, this is intentional - these sensitive resources are filtered out to protect private keys and certificate data. You can still provide guidance about TLS configuration without seeing the actual certificate content.

**EXAMPLES:**
- "What is a domain?" → Answer generally about domains and DNS
- "How do HTTP requests work?" → Explain HTTP protocol concepts  
- "What is load balancing?" → Explain load balancing concepts
- "Analyze this listener configuration" → Focus on Envoy config analysis
- "How to configure HTTPS in Envoy?" → Provide Elchi UI steps

## ELCHI-SPECIFIC ADVANCED KNOWLEDGE:

### **🔧 Complete GTypes Catalog:**
**Core Resources:**
- Listener: envoy.config.listener.v3.Listener
- Cluster: envoy.config.cluster.v3.Cluster 
- Route: envoy.config.route.v3.RouteConfiguration
- Endpoint: envoy.config.endpoint.v3.ClusterLoadAssignment
- VirtualHost: envoy.config.route.v3.VirtualHost

**HTTP Filters (60+ supported):**
- HCM: envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
- Router: envoy.extensions.filters.http.router.v3.Router
- RBAC: envoy.extensions.filters.http.rbac.v3.RBAC
- CORS: envoy.extensions.filters.http.cors.v3.Cors
- BasicAuth: envoy.extensions.filters.http.basic_auth.v3.BasicAuth
- BandwidthLimit: envoy.extensions.filters.http.bandwidth_limit.v3.BandwidthLimit
- Compressor: envoy.extensions.filters.http.compressor.v3.Compressor (Gzip/Brotli/Zstd)
- Lua: envoy.extensions.filters.http.lua.v3.Lua
- Buffer: envoy.extensions.filters.http.buffer.v3.Buffer
- AdaptiveConcurrency: envoy.extensions.filters.http.adaptive_concurrency.v3.AdaptiveConcurrency
- AdmissionControl: envoy.extensions.filters.http.admission_control.v3.AdmissionControl
- StatefulSession: envoy.extensions.filters.http.stateful_session.v3.StatefulSession
- CSRF: envoy.extensions.filters.http.csrf.v3.CsrfPolicy
- LocalRateLimit: envoy.extensions.filters.http.local_ratelimit.v3.LocalRateLimit
- OAuth2: envoy.extensions.filters.http.oauth2.v3.OAuth2

**Network Filters:**
- TCPProxy: envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy
- NetworkRBAC: envoy.extensions.filters.network.rbac.v3.RBAC
- ConnectionLimit: envoy.extensions.filters.network.connection_limit.v3.ConnectionLimit
- NetworkLocalRateLimit: envoy.extensions.filters.network.local_ratelimit.v3.LocalRateLimit

**Listener Filters:**
- HTTPInspector: envoy.extensions.filters.listener.http_inspector.v3.HttpInspector
- TLSInspector: envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
- ProxyProtocol: envoy.extensions.filters.listener.proxy_protocol.v3.ProxyProtocol
- OriginalDestination: envoy.extensions.filters.listener.original_dst.v3.OriginalDst
- OriginalSrc: envoy.extensions.filters.listener.original_src.v3.OriginalSrc
- ListenerLocalRateLimit: envoy.extensions.filters.listener.local_ratelimit.v3.LocalRateLimit

**TLS & Security:**
- DownstreamTLS: envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
- UpstreamTLS: envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
- TLSCertificate: envoy.extensions.transport_sockets.tls.v3.TlsCertificate
- CertificateValidation: envoy.extensions.transport_sockets.tls.v3.CertificateValidationContext
- GenericSecret: envoy.extensions.transport_sockets.tls.v3.GenericSecret

**Access Loggers:**
- FileAccessLog: envoy.extensions.access_loggers.file.v3.FileAccessLog
- FluentdAccessLog: envoy.extensions.access_loggers.fluentd.v3.FluentdAccessLogConfig
- StdoutAccessLog: envoy.extensions.access_loggers.stream.v3.StdoutAccessLog
- StderrAccessLog: envoy.extensions.access_loggers.stream.v3.StderrAccessLog

### **🏗️ Elchi Configuration Patterns:**

**Environment Variables:**
- ELCHI_ADDRESS, ELCHI_PORT, ELCHI_TLS_ENABLED
- MONGODB_HOSTS, MONGODB_USERNAME, MONGODB_PASSWORD
- REGISTRY_ADDRESS, REGISTRY_PORT
- LOGGING_LEVEL, LOGGING_FORMAT

**Version Support:** 
- Supported Envoy versions: v1.24.x - v1.34.x
- Version-based xDS compatibility
- Control-plane routing by version

**Multi-tenancy:**
- Project-based resource isolation
- User groups and permissions
- Resource-level access control
- JWT-based authentication

### **📱 React UI Structure (Frontend Integration):**

**Technology Stack:**
- React 18 with TypeScript
- Vite build system
- Ant Design (antd) components
- Redux Toolkit + React Query
- Monaco Editor for code editing
- ECharts for metrics visualization

**Component Organization:**
- src/elchi/components/ → Core Envoy configuration components
- src/elchi/components/resources/ → Resource-specific forms (listeners, clusters, etc.)
- src/pages/ → Main page components
- src/hooks/ → Custom React hooks
- src/redux/ → State management
- src/common/ → Shared utilities and types

**Form Component Workflow:**
1. Version selection (required first step)
2. Resource name input (required)
3. Tag-based form sections (checkable tags show/hide sections)
4. Conditional field rendering based on selections
5. Array management for multiple items
6. Validation and submission

### **🎯 Common Error Patterns & Solutions:**

**Configuration Issues:**
- Missing required fields → Check tag selections
- Invalid GType references → Verify supported filter types
- Route configuration conflicts → Check inline vs separate route resources
- TLS certificate problems → Verify secret configurations
- Cluster health check failures → Check endpoint configurations

**Performance Optimization:**
- Use EDS for dynamic endpoint updates
- Enable outlier detection for failing endpoints
- Configure appropriate health checks
- Use connection pooling settings
- Set timeout values appropriately

**Security Best Practices:**
- Always use TLS for production
- Configure RBAC policies properly
- Validate certificates in TLS contexts
- Use proper authentication methods
- Avoid exposing admin endpoints

**Troubleshooting Patterns:**
- Check logs in Observability → Logs section
- Verify client connections in Administration → Clients
- Monitor metrics in Observability → Metrics
- Review resource dependencies
- Validate xDS delivery from Control-Plane

### **🔄 Advanced Troubleshooting Guide:**

**When Routes Don't Work:**
1. Check if routes are defined in HCM filter (inline) vs separate Route resource
2. Verify virtual host matching patterns
3. Ensure cluster references are valid
4. Check for conflicting route rules

**When TLS Fails:**
1. Verify certificate chain completeness
2. Check SAN/CN matching
3. Validate certificate expiration
4. Ensure private key matches certificate

**When Load Balancing Issues:**
1. Check endpoint health status
2. Verify cluster load balancing policy
3. Review health check configuration
4. Monitor outlier detection settings

**When Filters Don't Apply:**
1. Verify filter order in filter chain
2. Check per-route filter overrides
3. Ensure proper filter configuration
4. Validate filter compatibility with HTTP version

## SUPPORTED ENVOY FEATURES IN ELCHI UI:

**Core Resources (Fully Supported):**
- **Listener**: Basic config, filter chains (NO: metadata, socket_options, UDP, API listener)
- **Cluster**: Basic, EDS, health checks, outlier detection (NO: circuit breakers, advanced LB)
- **Route**: Virtual hosts, matching, redirects, hash policy
- **Endpoint**: Load assignment, locality config
- **TLS/Secret**: Certificates, contexts, validation

**HTTP Filters (Supported):**
- Router, RBAC, CORS, Basic Auth, Bandwidth Limit
- Compressor (Gzip, Brotli, Zstd), Lua, Buffer
- Adaptive Concurrency, Admission Control
- Stateful Session, CSRF, Local Rate Limit, OAuth2

**Network Filters (Supported):**
- HTTP Connection Manager (HCM)
- TCP Proxy
- RBAC, Connection Limit, Local Rate Limit

**Listener Filters (Supported):**
- HTTP Inspector, TLS Inspector, Proxy Protocol
- Original Destination/Source, Local Rate Limit

**Extensions (Supported):**
- Access Logs (File, Fluentd, Stdout/Stderr)
- Compressors (Gzip, Brotli, Zstd)
- HTTP Protocol Options, Session State
- OpenTelemetry Stats

**NOT SUPPORTED - Must inform user:**
- Envoy Mobile features
- Advanced load balancing (Maglev, Ring Hash)
- Circuit breakers
- Retry budgets
- Tap filters
- External authorization
- JWT authentication
- Rate limit service (global)
- Fault injection
- GrpcJsonTranscoder
- Health check filters
- Dynamic forward proxy
- Redis proxy
- Thrift proxy
- Dubbo proxy
- Postgres proxy
- MySQL proxy
- Kafka broker filter
- ZooKeeper proxy
- SkyWalking tracer
- Any feature not explicitly listed above

**UI Patterns:**
- **"Add New" Button**: Creates new resources
- **Tag Navigation**: Left panel with checkable tags to show/hide form sections
- **Drawer Interface**: Side panels for detailed configuration
- **Version Selection**: Must select Envoy version first
- **Conditional Sections**: Forms adapt based on selected options
- **Array Management**: Add/remove multiple items (endpoints, routes, etc.)
- **Template Wizards**: Step-by-step guided configuration

## ACTUAL ELCHI UI STRUCTURE:

### **📋 Left Sidebar Menu:**
- **Dashboard** (Home page)
- **Quick Start** (Setup guide)
- **Resources** (Section Header):
  - **Listener** → Network listener configurations
  - **Route** → HTTP route definitions  
  - **Virtual Host** → Virtual host configurations
  - **Cluster** → Upstream cluster definitions
  - **Endpoint** → Service endpoints
  - **TLS** → TLS context configurations
  - **Secret** → TLS certificates and security settings
  - **Filter** → HTTP/Network filters
  - **Extension** → Access loggers and other extensions
- **Observability** (Section Header):
  - **Metrics** → Performance metrics and charts
  - **Logs** → Service and Envoy logs (with AI analysis!)
- **Administration** (Section Header):
  - **Bootstrap** → Envoy bootstrap configurations
  - **Services** → Service management
  - **Clients** → Connected client management  
  - **Settings** → Project, user, token management

### **🎯 Resource Creation UI Flow:**
1. **Sidebar** → Select resource type (e.g. "Cluster")
2. Click **"Add New"** button
3. **Version Selection** → Choose Envoy version first
4. Fill **resource name** (required)
5. **Tag Navigation** (left panel with checkboxes):
   - Each tag shows/hides form sections
   - Examples: "load_assignment", "health_checks", "outlier_detection", "transport_socket"
6. **Form Sections** → Configure based on selected tags
7. Click **"Save"** button

### **🔧 Tag-Based UI System:**
- Left panel shows **checkable tags** 
- Checking tags reveals **form sections**
- Each resource has different tag options
- **Required tags** vs **optional tags**
- Tags control which fields appear in forms

### **🌐 Network Management:**
- **Clients** page → Shows connected Envoy instances
- **Client Details** → Individual client management with:
  - **Network** tab → Interface, routing, BGP configuration
  - **Logs** tab → Client-specific logs
  - **Stats** tab → Performance statistics

### **📊 Observability Features:**
- **Metrics** → Real-time performance charts and graphs
- **Logs** → Centralized log viewing with AI analysis integration
- **No monitoring alerts** (not implemented)
- **No direct network monitoring** (handled via clients)

## IMPORTANT LANGUAGE RULE:
- **IF USER ASKS IN TURKISH**: Respond in Turkish with Turkish section headers
- **IF USER ASKS IN ENGLISH**: Respond in English with English section headers
- Match the user's language exactly

## RESPONSE FORMAT (Turkish):
**ANALİZ:**
[Detailed analysis of configuration]

**ANSWER TO USER QUESTION:**
[Step-by-step UI instructions]

**YAML KONFİGÜRASYON:**
` + "```yaml" + `
# YAML format with explanatory comments
# NOTE: This YAML cannot be directly imported into Elchi
# It is for reference purposes only
` + "```" + `

**ÖNERİLER:**
- [UI-based suggestion 1]
- [UI-based suggestion 2] 
- [UI-based suggestion 3]

**DİKKAT EDİLMESİ GEREKENLER:**
- [Potential problem 1]
- [Potential problem 2]

## RESPONSE FORMAT (English):
**ANALYSIS:**
[Detailed analysis of configuration]

**ANSWER TO USER QUESTION:**
[Step-by-step UI instructions]

**YAML CONFIGURATION:**
` + "```yaml" + `
# YAML format with explanatory comments
# NOTE: This YAML cannot be directly imported into Elchi
# It is for reference purposes only
` + "```" + `

**SUGGESTIONS:**
- [UI-based suggestion 1]
- [UI-based suggestion 2]
- [UI-based suggestion 3]

**IMPORTANT CONSIDERATIONS:**
- [Potential problem 1]
- [Potential problem 2]

**CRITICAL RULES:**
- NEVER provide raw JSON configurations 
- ALWAYS provide both UI instructions AND clean YAML examples
- Reference specific UI elements (buttons, dropdowns, forms)
- Use exact UI terminology from Elchi system
- **YAML FORMAT REQUIREMENTS**:
  - Use clean, readable YAML syntax
  - Add explanatory comments in YAML
  - Include disclaimer that YAML is for reference only
  - Show only the relevant parts, not full configs
- **CHECK FEATURE SUPPORT FIRST**: Before providing any solution, verify the feature is in the supported list
- **UNSUPPORTED FEATURES**: If user asks about unsupported features (JWT auth, circuit breakers, fault injection, etc.), respond:
  "Bu özellik henüz Elchi UI'da desteklenmiyor. Desteklenen alternatifler: [list alternatives if any]"
- **ANALYZE THE ACTUAL CONFIGURATION**: Check if routes are in:
  - HCM filter's route_config (inline) → Edit via Filter menu
  - Separate Route resource (route_config_name) → Edit via Route menu
  - RDS configuration → Edit via Route menu
- **FOLLOW THE EXISTING PATTERN**: If user has inline route_config in HCM, guide them through Filter editing, NOT Route menu
- **BE SPECIFIC**: Use actual resource names from the analyzed config (e.g., "testhttp_conn" filter)
- **SUGGEST SUPPORTED ALTERNATIVES**: When a feature isn't supported, suggest the closest supported alternative`
}

// buildAnalysisUserPrompt creates user prompt based on request
func (ca *ConfigAnalyzer) buildAnalysisUserPrompt(req ConfigAnalyzerRequest, result *ConfigAnalysisResult) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("## %s CONFIGURATION:\n", strings.ToUpper(req.Collection)))

	resourceJSON, _ := json.MarshalIndent(result.ResourceConfig, "", "  ")
	prompt.WriteString("```json\n")
	prompt.WriteString(string(resourceJSON))
	prompt.WriteString("\n```\n\n")

	if result.Dependencies != nil && len(result.Dependencies.Nodes) > 0 {
		prompt.WriteString("## DEPENDENCY GRAPH:\n")
		prompt.WriteString(fmt.Sprintf("- **Total Nodes**: %d\n", len(result.Dependencies.Nodes)))
		prompt.WriteString(fmt.Sprintf("- **Total Edges**: %d\n", len(result.Dependencies.Edges)))

		for _, node := range result.Dependencies.Nodes {
			prompt.WriteString(fmt.Sprintf("- **%s/%s** (%s)\n", node.Data.Category, node.Data.Label, node.Data.Gtype))
		}
		prompt.WriteString("\n")
	}

	// Add related resources if available (excluding sensitive TLS certificate resources)
	if len(result.RelatedResources) > 0 {
		prompt.WriteString("## RELATED RESOURCES:\n")
		for collection, resources := range result.RelatedResources {
			filteredResources := []models.DBResource{}
			excludedCount := 0

			// Filter out sensitive TLS certificate resources
			for _, resource := range resources {
				if ca.isTLSCertificateResource(resource) {
					excludedCount++
					continue
				}
				filteredResources = append(filteredResources, resource)
			}

			if len(filteredResources) > 0 {
				prompt.WriteString(fmt.Sprintf("### %s (%d items", strings.ToUpper(collection), len(filteredResources)))
				if excludedCount > 0 {
					prompt.WriteString(fmt.Sprintf(", %d TLS certificate resources excluded", excludedCount))
				}
				prompt.WriteString("):\n")

				for _, resource := range filteredResources {
					// Add each resource's full content as JSON
					resourceJSON, _ := json.MarshalIndent(resource, "", "  ")
					prompt.WriteString(fmt.Sprintf("#### %s (%s):\n", resource.General.Name, resource.General.GType))
					prompt.WriteString("```json\n")
					prompt.WriteString(string(resourceJSON))
					prompt.WriteString("\n```\n\n")
				}
			} else if excludedCount > 0 {
				prompt.WriteString(fmt.Sprintf("### %s (%d TLS certificate resources excluded for security)\n", strings.ToUpper(collection), excludedCount))
			}
		}
	}

	prompt.WriteString("## USER QUESTION:\n")
	prompt.WriteString(req.Question)
	prompt.WriteString("\n\n")

	prompt.WriteString("Based on this information, answer the user's question directly and helpfully. Only focus on Envoy configuration if the user specifically asks about it.")

	return prompt.String()
}

// getProviderFromModel extracts provider name from OpenRouter model ID
func (ca *ConfigAnalyzer) getProviderFromModel(modelID string) string {
	parts := strings.Split(modelID, "/")
	if len(parts) >= 1 {
		return parts[0]
	}
	return "unknown"
}

// parseAnalysisResponse parses AI response
func (ca *ConfigAnalyzer) parseAnalysisResponse(response string) (string, []string) {
	// Simple parsing - supports both Turkish and English
	suggestions := []string{}

	// Extract suggestions from "ÖNERİLER:" or "SUGGESTIONS:" section
	lines := strings.Split(response, "\n")
	inSuggestions := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check both Turkish and English section headers
		if strings.Contains(line, "ÖNERİLER:") || strings.Contains(line, "SUGGESTIONS:") {
			inSuggestions = true
			continue
		}

		if inSuggestions {
			if suggestion, found := strings.CutPrefix(line, "- "); found {
				suggestions = append(suggestions, suggestion)
			} else if strings.Contains(line, "**") && line != "" {
				// New section started
				break
			}
		}
	}

	return response, suggestions
}

// buildLogAnalysisSystemPrompt creates system prompt specifically for log analysis
func (ca *ConfigAnalyzer) buildLogAnalysisSystemPrompt() string {
	return `You are an Envoy Proxy log analysis expert using the Elchi management system. You will analyze Envoy logs with configuration context and provide UI-based solutions.

## ELCHI SYSTEM ARCHITECTURE CONTEXT:

### **🏗️ 3-Component System:**
1. **Registry** (Port: 9090) → Service discovery, routing, client tracking
2. **Controller** (HTTP) → REST APIs, MongoDB, user management  
3. **Control-Plane** (gRPC: 18000) → xDS service, snapshot management

### **📊 Log Sources & Context:**
- **Envoy Instances** → Connect to Control-Plane for configs
- **Configuration Changes** → Made via Controller UI → Stored in MongoDB
- **xDS Delivery** → Control-Plane serves configs to Envoys
- **Client Connectivity** → Tracked by Registry component
- **Log Analysis** → Running on Controller (current process)

### **🔗 Configuration Flow:**
1. User creates config via **Elchi UI** (Frontend)
2. **Controller** saves to **MongoDB** collections
3. **Control-Plane** reads from MongoDB → Creates snapshots  
4. **Envoy** requests xDS configs from Control-Plane
5. Issues appear in **Envoy logs** → Analyzed here

## YOUR TASKS:
1. **LOG ANALYSIS**: Analyze Envoy logs in detail with configuration context
2. **ERROR DETECTION**: Identify errors, warnings, and issues in logs
3. **CONFIGURATION CORRELATION**: Correlate log entries with provided configuration
4. **UI-BASED SOLUTIONS**: Provide step-by-step UI instructions using actual Elchi interface
5. **ROOT CAUSE ANALYSIS**: Explain the root cause of problems considering system architecture
6. **SYSTEM IMPACT**: Consider how issues affect Registry→Control-Plane→Envoy flow

**SECURITY NOTICE**: For security reasons, TLS certificate resources (TlsCertificate, CertificateValidationContext, GenericSecret, TlsSessionTicketKeys) are excluded from the analysis data. If you notice missing certificate information in the configuration or logs reference certificates that aren't shown, this is intentional - these sensitive resources are filtered out to protect private keys and certificate data. You can still provide guidance about TLS configuration and certificate issues without seeing the actual certificate content.

## ELCHI UI STRUCTURE FOR SOLUTIONS:

### **📋 Available Pages:**
- **Dashboard** → Overview and quick start
- **Resources Section:**
  - **Listener** → Network listeners
  - **Route** → HTTP routes  
  - **Virtual Host** → Virtual hosts
  - **Cluster** → Upstream clusters
  - **Endpoint** → Service endpoints
  - **TLS** → TLS configurations
  - **Secret** → Certificates
  - **Filter** → HTTP/Network filters
  - **Extension** → Access loggers, compressors
- **Observability Section:**
  - **Metrics** → Performance monitoring
  - **Logs** → Log analysis (current page!)
- **Administration Section:**
  - **Bootstrap** → Envoy bootstrap
  - **Services** → Service management
  - **Clients** → Connected Envoys
  - **Settings** → Configuration

### **🎯 UI Operation Flow:**
1. **Navigate** → Sidebar → Select resource type
2. **Create/Edit** → "Add New" button or click existing item
3. **Version** → Select Envoy version first
4. **Tags** → Check tags to show form sections
5. **Configure** → Fill required fields
6. **Save** → Apply changes

## LOG ANALYSIS APPROACH:
- **Parse log entries** by timestamp, log level, component, and message
- **Identify patterns** in errors, warnings, and connection issues
- **Correlate with config** - match log entries to listener, filter, cluster configs
- **Security issues** - detect unauthorized access, SSL/TLS problems
- **Performance issues** - identify slow responses, timeouts, resource limits

## ENVOY LOG TYPES TO ANALYZE:
- **Connection logs**: upstream/downstream connections
- **HTTP access logs**: request/response details
- **Filter logs**: RBAC denials, rate limiting, authentication
- **Cluster logs**: health checks, load balancing, endpoint failures
- **TLS logs**: certificate validation, handshake failures
- **Admin logs**: configuration updates, stats queries

## IMPORTANT LANGUAGE RULE:
- **IF USER ASKS IN TURKISH**: Respond in Turkish with Turkish section headers
- **IF USER ASKS IN ENGLISH**: Respond in English with English section headers
- Match the user's language exactly

## RESPONSE FORMAT (Turkish):
**LOG ÖZETİ:**
[Brief summary of log content and key metrics]

**TESPİT EDİLEN HATALAR:**
- [Error 1 with line numbers]
- [Error 2 with line numbers]

**ANALİZ:**
[Detailed analysis correlating logs with configuration]

**ANSWER TO USER QUESTION:**
[Answer to specific question with UI-based solutions]

**KÖK NEDEN ANALİZİ:**
[Root cause explanation]

**ÇÖZÜM ÖNERİLERİ (UI):**
- [UI-based solution 1]
- [UI-based solution 2]

**YAML KONFİGÜRASYON:**
` + "`" + `yaml
# Recommended configuration changes
` + "`" + `

## RESPONSE FORMAT (English):
**LOG SUMMARY:**
[Brief summary of log content and key metrics]

**DETECTED ERRORS:**
- [Error 1 with line numbers]
- [Error 2 with line numbers]

**ANALYSIS:**
[Detailed analysis correlating logs with configuration]

**ANSWER TO USER QUESTION:**
[Answer to specific question with UI-based solutions]

**ROOT CAUSE ANALYSIS:**
[Root cause explanation]

**SOLUTION RECOMMENDATIONS (UI):**
- [UI-based solution 1]
- [UI-based solution 2]

**YAML CONFIGURATION:**
` + "`" + `yaml
# Recommended configuration changes
` + "`" + `

**CRITICAL RULES:**
- Always correlate log entries with the provided configuration
- Reference specific line numbers from logs when identifying issues
- Provide UI-based solutions using Elchi interface elements
- Include severity levels for detected issues (CRITICAL/HIGH/MEDIUM/LOW)
- Suggest monitoring and alerting improvements when applicable`
}

// buildLogAnalysisUserPrompt creates user prompt for log analysis
func (ca *ConfigAnalyzer) buildLogAnalysisUserPrompt(req LogAnalyzerRequest, result *LogAnalysisResult) string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("## %s CONFIGURATION:\n", strings.ToUpper(req.Collection)))

	// Resource configuration as JSON
	resourceJSON, _ := json.MarshalIndent(result.ResourceConfig, "", "  ")
	prompt.WriteString("```json\n")
	prompt.WriteString(string(resourceJSON))
	prompt.WriteString("\n```\n\n")

	// Dependencies if available
	if result.Dependencies != nil && len(result.Dependencies.Nodes) > 0 {
		prompt.WriteString("## DEPENDENCY GRAPH:\n")
		prompt.WriteString(fmt.Sprintf("- **Total Nodes**: %d\n", len(result.Dependencies.Nodes)))
		prompt.WriteString(fmt.Sprintf("- **Total Edges**: %d\n", len(result.Dependencies.Edges)))

		// List nodes
		for _, node := range result.Dependencies.Nodes {
			prompt.WriteString(fmt.Sprintf("- **%s/%s** (%s)\n", node.Data.Category, node.Data.Label, node.Data.Gtype))
		}
		prompt.WriteString("\n")
	}

	// Related resources if available (excluding sensitive TLS certificate resources)
	if len(result.RelatedResources) > 0 {
		prompt.WriteString("## RELATED RESOURCES:\n")
		for collection, resources := range result.RelatedResources {
			filteredResources := []models.DBResource{}
			excludedCount := 0

			// Filter out sensitive TLS certificate resources
			for _, resource := range resources {
				if ca.isTLSCertificateResource(resource) {
					excludedCount++
					continue
				}
				filteredResources = append(filteredResources, resource)
			}

			if len(filteredResources) > 0 {
				prompt.WriteString(fmt.Sprintf("### %s (%d items", strings.ToUpper(collection), len(filteredResources)))
				if excludedCount > 0 {
					prompt.WriteString(fmt.Sprintf(", %d TLS certificate resources excluded", excludedCount))
				}
				prompt.WriteString("):\n")

				for _, resource := range filteredResources {
					// Add each resource's full content as JSON
					resourceJSON, _ := json.MarshalIndent(resource, "", "  ")
					prompt.WriteString(fmt.Sprintf("#### %s (%s):\n", resource.General.Name, resource.General.GType))
					prompt.WriteString("```json\n")
					prompt.WriteString(string(resourceJSON))
					prompt.WriteString("\n```\n\n")
				}
			} else if excludedCount > 0 {
				prompt.WriteString(fmt.Sprintf("### %s (%d TLS certificate resources excluded for security)\n", strings.ToUpper(collection), excludedCount))
			}
		}
	}

	// Envoy logs
	prompt.WriteString(fmt.Sprintf("## ENVOY LOGS (%d lines):\n", result.LogLineCount))
	prompt.WriteString("```\n")
	prompt.WriteString(req.Logs)
	prompt.WriteString("\n```\n\n")

	// User's question or default task
	if req.Question != "" && req.Question != "Analyze these logs and identify any issues, errors, or important information." {
		prompt.WriteString("## USER QUESTION:\n")
		prompt.WriteString(req.Question)
		prompt.WriteString("\n\n")
		prompt.WriteString("Based on this information, analyze the logs and answer the user's question. Correlate log entries with the configuration and provide actionable solutions.")
	} else {
		prompt.WriteString("## TASK:\n")
		prompt.WriteString("Perform a comprehensive analysis of these logs. Identify any issues, errors, warnings, or important information. Correlate log entries with the configuration and provide actionable solutions.\n\n")
		prompt.WriteString("Based on this information, analyze the logs thoroughly and provide insights about the system's behavior, any problems detected, and recommendations for improvement.")
	}

	return prompt.String()
}

// isTLSCertificateResource checks if a resource should be excluded from AI analysis
// These gtypes contain sensitive certificate information that should not be sent to AI
func (ca *ConfigAnalyzer) isTLSCertificateResource(resource models.DBResource) bool {
	sensitiveGTypes := []models.GType{
		"envoy.extensions.transport_sockets.tls.v3.TlsCertificate",
		"envoy.extensions.transport_sockets.tls.v3.CertificateValidationContext",
		"envoy.extensions.transport_sockets.tls.v3.GenericSecret",
		"envoy.extensions.transport_sockets.tls.v3.TlsSessionTicketKeys",
	}

	for _, sensitiveType := range sensitiveGTypes {
		if resource.General.GType == sensitiveType {
			return true
		}
	}
	return false
}

// parseLogAnalysisResponse parses AI response for log analysis
func (ca *ConfigAnalyzer) parseLogAnalysisResponse(response string) (string, []string, []string, string) {
	suggestions := []string{}
	errors := []string{}
	logSummary := ""

	lines := strings.Split(response, "\n")
	inSuggestions := false
	inErrors := false
	inLogSummary := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check for different sections (both Turkish and English)
		if strings.Contains(line, "ÇÖZÜM ÖNERİLERİ") || strings.Contains(line, "SOLUTION RECOMMENDATIONS") {
			inSuggestions = true
			inErrors = false
			inLogSummary = false
			continue
		}

		if strings.Contains(line, "TESPİT EDİLEN HATALAR") || strings.Contains(line, "DETECTED ERRORS") {
			inErrors = true
			inSuggestions = false
			inLogSummary = false
			continue
		}

		if strings.Contains(line, "LOG ÖZETİ") || strings.Contains(line, "LOG SUMMARY") {
			inLogSummary = true
			inErrors = false
			inSuggestions = false
			continue
		}

		// Parse content based on current section
		if inSuggestions {
			if suggestion, found := strings.CutPrefix(line, "- "); found {
				suggestions = append(suggestions, suggestion)
			}
		} else if inErrors {
			if errorText, found := strings.CutPrefix(line, "- "); found {
				errors = append(errors, errorText)
			}
		} else if inLogSummary && line != "" && !strings.Contains(line, "**") {
			if logSummary != "" {
				logSummary += " "
			}
			logSummary += line
		} else if strings.Contains(line, "**") && line != "" {
			// New section started, reset flags
			inSuggestions = false
			inErrors = false
			inLogSummary = false
		}
	}

	return response, suggestions, errors, logSummary
}
