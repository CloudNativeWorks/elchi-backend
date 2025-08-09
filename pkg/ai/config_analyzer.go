package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/controller/dependency"
	"github.com/CloudNativeWorks/elchi-backend/pkg/db"
	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
)

// Claude API structs
type ClaudeAPIClient struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type ClaudeRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	System    []ClaudeSystemBlock `json:"system,omitempty"`
	Messages  []ClaudeMessage     `json:"messages"`
}

type ClaudeSystemBlock struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text"`
	CacheControl *ClaudeCacheControl `json:"cache_control,omitempty"`
}

type ClaudeCacheControl struct {
	Type string `json:"type"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ClaudeResponse struct {
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content"`
	Model string `json:"model"`
	Role  string `json:"role"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// NewClaudeClient creates a new Claude API client
func NewClaudeClient(apiKey string) *ClaudeAPIClient {
	return &ClaudeAPIClient{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com/v1/messages",
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// ConfigAnalyzerRequest represents user's analyzer request
type ConfigAnalyzerRequest struct {
	// Resource information (required)
	ResourceName string `json:"resource_name" validate:"required"`
	Collection   string `json:"collection" validate:"required"`   // listeners, filters, clusters, routes, etc.
	Project      string `json:"project" validate:"required"`
	Version      string `json:"version" validate:"required"`
	
	// User question
	Question string `json:"question" validate:"required"`
	
	// Optional context
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	Depth              int  `json:"depth,omitempty"` // Dependency depth (default: 3)
}

// LogAnalyzerRequest represents user's log analysis request
type LogAnalyzerRequest struct {
	// Resource information (required)
	ResourceName string `json:"resource_name" validate:"required"`
	Collection   string `json:"collection" validate:"required"`   // listeners, filters, clusters, routes, etc.
	Project      string `json:"project" validate:"required"`
	
	// Log data
	Logs string `json:"logs" validate:"required"`
	
	// User question about logs (optional - if empty, general analysis will be performed)
	Question string `json:"question,omitempty"`
	
	// Optional context
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	Depth              int  `json:"depth,omitempty"` // Dependency depth (default: 3)
}

// ConfigAnalysisResult represents analysis result
type ConfigAnalysisResult struct {
	ResourceConfig   models.DBResource              `json:"resource_config"`
	Dependencies     *dependency.Graph              `json:"dependencies,omitempty"`
	RelatedResources map[string][]models.DBResource `json:"related_resources"`
	Analysis         string                         `json:"analysis"`
	Suggestions      []string                       `json:"suggestions,omitempty"`
	Warnings         []string                       `json:"warnings,omitempty"`
	ProcessedAt      time.Time                     `json:"processed_at"`
	TokenUsage       TokenUsage                    `json:"token_usage"`
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
	ProcessedAt      time.Time                     `json:"processed_at"`
	TokenUsage       TokenUsage                    `json:"token_usage"`
}

// ConfigAnalyzer analyzes resources and their dependencies
type ConfigAnalyzer struct {
	dbContext         *db.AppContext
	dependencyHandler *dependency.AppHandler
	aiClient          *ClaudeAPIClient
	logger            *logrus.Logger
	systemPrompt      string // Cached system prompt
	usageTracker      *UsageTracker
}

func NewConfigAnalyzer(dbContext *db.AppContext, aiClient *ClaudeAPIClient, logger *logrus.Logger) *ConfigAnalyzer {
	dependencyHandler := dependency.NewDependencyHandler(dbContext)
	usageTracker := NewUsageTracker(dbContext)
	
	analyzer := &ConfigAnalyzer{
		dbContext:         dbContext,
		dependencyHandler: dependencyHandler,
		usageTracker:      usageTracker,
		aiClient:          aiClient,
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
		// Record failed usage
		ca.recordUsage(ctx, AIUsageRecord{
			Project:      req.Project,
			UserID:       userID,
			RequestType:  "analyze",
			ResourceName: req.ResourceName,
			Collection:   req.Collection,
			Success:      false,
			ErrorMessage: err.Error(),
			Duration:     time.Since(startTime).Milliseconds(),
		})
		return nil, fmt.Errorf("AI analysis failed: %w", err)
	}

	result.Analysis = analysis
	result.Suggestions = suggestions
	result.TokenUsage = TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		CostUSD:      calculateTokenCost(inputTokens, outputTokens),
	}

	// Record successful usage
	ca.recordUsage(ctx, AIUsageRecord{
		Project:      req.Project,
		UserID:       userID,
		RequestType:  "analyze",
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
	analysis, suggestions, errors, logSummary, inputTokens, outputTokens, err := ca.analyzeLogsWithAI(req, result)
	if err != nil {
		// Record failed usage
		ca.recordUsage(ctx, AIUsageRecord{
			Project:      req.Project,
			UserID:       userID,
			RequestType:  "analyze-logs",
			ResourceName: req.ResourceName,
			Collection:   req.Collection,
			Success:      false,
			ErrorMessage: err.Error(),
			Duration:     time.Since(startTime).Milliseconds(),
		})
		return nil, fmt.Errorf("AI log analysis failed: %w", err)
	}

	result.Analysis = analysis
	result.Suggestions = suggestions
	result.ErrorsDetected = errors
	result.LogSummary = logSummary
	result.TokenUsage = TokenUsage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		CostUSD:      calculateTokenCost(inputTokens, outputTokens),
	}

	// Record successful usage
	ca.recordUsage(ctx, AIUsageRecord{
		Project:      req.Project,
		UserID:       userID,
		RequestType:  "analyze-logs",
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
func (ca *ConfigAnalyzer) getGTypeFromCollection(collection string) models.GTypes {
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

	// Call Claude API - Cache system prompt
	claudeReq := ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4000,
		System: []ClaudeSystemBlock{
			{
				Type: "text",
				Text: ca.systemPrompt, // Cache'lenmiş system prompt kullan
				CacheControl: &ClaudeCacheControl{
					Type: "ephemeral",
				},
			},
		},
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: userPrompt,
			},
		},
	}

	// AI client'tan response al
	response, inputTokens, outputTokens, err := ca.callClaudeAPI(claudeReq)
	if err != nil {
		return "", nil, 0, 0, err
	}

	// Parse response
	analysis, suggestions := ca.parseAnalysisResponse(response)
	
	return analysis, suggestions, inputTokens, outputTokens, nil
}

// analyzeLogsWithAI analyzes Envoy logs with configuration context using AI
func (ca *ConfigAnalyzer) analyzeLogsWithAI(req LogAnalyzerRequest, result *LogAnalysisResult) (string, []string, []string, string, int, int, error) {
	// Build user prompt for log analysis (system prompt is already cached)
	userPrompt := ca.buildLogAnalysisUserPrompt(req, result)

	// Call Claude API - Use cached system prompt
	claudeReq := ClaudeRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4000,
		System: []ClaudeSystemBlock{
			{
				Type: "text",
				Text: ca.buildLogAnalysisSystemPrompt(), // Use log-specific system prompt
				CacheControl: &ClaudeCacheControl{
					Type: "ephemeral",
				},
			},
		},
		Messages: []ClaudeMessage{
			{
				Role:    "user",
				Content: userPrompt,
			},
		},
	}

	// Get response from AI client
	response, inputTokens, outputTokens, err := ca.callClaudeAPI(claudeReq)
	if err != nil {
		return "", nil, nil, "", 0, 0, err
	}

	// Parse response
	analysis, suggestions, errors, logSummary := ca.parseLogAnalysisResponse(response)
	
	return analysis, suggestions, errors, logSummary, inputTokens, outputTokens, nil
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

**EXAMPLES:**
- "What is a domain?" → Answer generally about domains and DNS
- "How do HTTP requests work?" → Explain HTTP protocol concepts  
- "What is load balancing?" → Explain load balancing concepts
- "Analyze this listener configuration" → Focus on Envoy config analysis
- "How to configure HTTPS in Envoy?" → Provide Elchi UI steps

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
	
	// Resource config'ini JSON olarak ekle
	resourceJSON, _ := json.MarshalIndent(result.ResourceConfig, "", "  ")
	prompt.WriteString("```json\n")
	prompt.WriteString(string(resourceJSON))
	prompt.WriteString("\n```\n\n")

	// Dependencies varsa ekle
	if result.Dependencies != nil && len(result.Dependencies.Nodes) > 0 {
		prompt.WriteString("## DEPENDENCY GRAPH:\n")
		prompt.WriteString(fmt.Sprintf("- **Total Nodes**: %d\n", len(result.Dependencies.Nodes)))
		prompt.WriteString(fmt.Sprintf("- **Total Edges**: %d\n", len(result.Dependencies.Edges)))
		
		// Node'ları listele
		for _, node := range result.Dependencies.Nodes {
			prompt.WriteString(fmt.Sprintf("- **%s/%s** (%s)\n", node.Data.Category, node.Data.Label, node.Data.Gtype))
		}
		prompt.WriteString("\n")
	}

	// Add related resources if available
	if len(result.RelatedResources) > 0 {
		prompt.WriteString("## RELATED RESOURCES:\n")
		for collection, resources := range result.RelatedResources {
			prompt.WriteString(fmt.Sprintf("### %s (%d items):\n", strings.ToUpper(collection), len(resources)))
			for _, resource := range resources {
				// Add each resource's full content as JSON
				resourceJSON, _ := json.MarshalIndent(resource, "", "  ")
				prompt.WriteString(fmt.Sprintf("#### %s (%s):\n", resource.General.Name, resource.General.GType))
				prompt.WriteString("```json\n")
				prompt.WriteString(string(resourceJSON))
				prompt.WriteString("\n```\n\n")
			}
		}
	}

	// Kullanıcının sorusunu ekle
	prompt.WriteString("## USER QUESTION:\n")
	prompt.WriteString(req.Question)
	prompt.WriteString("\n\n")

	prompt.WriteString("Based on this information, answer the user's question directly and helpfully. Only focus on Envoy configuration if the user specifically asks about it.")

	return prompt.String()
}

// callClaudeAPI calls the Claude API
func (ca *ConfigAnalyzer) callClaudeAPI(req ClaudeRequest) (string, int, int, error) {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", ca.aiClient.BaseURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return "", 0, 0, fmt.Errorf("create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", ca.aiClient.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := ca.aiClient.HTTPClient.Do(httpReq)
	if err != nil {
		return "", 0, 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", 0, 0, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var response ClaudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", 0, 0, fmt.Errorf("decode response: %w", err)
	}

	if len(response.Content) == 0 {
		return "", 0, 0, fmt.Errorf("empty response content")
	}

	return response.Content[0].Text, response.Usage.InputTokens, response.Usage.OutputTokens, nil
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

	// Related resources if available
	if len(result.RelatedResources) > 0 {
		prompt.WriteString("## RELATED RESOURCES:\n")
		for collection, resources := range result.RelatedResources {
			prompt.WriteString(fmt.Sprintf("### %s (%d items):\n", strings.ToUpper(collection), len(resources)))
			for _, resource := range resources {
				// Add each resource's full content as JSON
				resourceJSON, _ := json.MarshalIndent(resource, "", "  ")
				prompt.WriteString(fmt.Sprintf("#### %s (%s):\n", resource.General.Name, resource.General.GType))
				prompt.WriteString("```json\n")
				prompt.WriteString(string(resourceJSON))
				prompt.WriteString("\n```\n\n")
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