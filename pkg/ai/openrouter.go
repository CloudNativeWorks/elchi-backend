package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelInfo represents an available AI model from OpenRouter
type ModelInfo struct {
	ID              string `json:"id"`               // e.g., "meta-llama/llama-3.1-8b-instruct:free"
	Name            string `json:"name"`             // Human readable name
	ContextLength   int    `json:"context_length"`   // Maximum context tokens
	PromptPrice     string `json:"prompt_price"`     // Price per token for prompts
	CompletionPrice string `json:"completion_price"` // Price per token for completions
	SupportsSystem  bool   `json:"supports_system"`  // System prompt support
	IsFree          bool   `json:"is_free"`          // Free model flag
}

// OpenRouterError represents an error response from OpenRouter API
type OpenRouterError struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	Model      string `json:"model"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	RawBody    string `json:"raw_body"`
}

func (e *OpenRouterError) Error() string {
	return fmt.Sprintf("OpenRouter API error (%d): %s. Model: %s", e.StatusCode, e.Message, e.Model)
}

// OpenRouterClient handles all AI requests via OpenRouter.ai
type OpenRouterClient struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	models     []ModelInfo // Cached model list
}

// OpenRouterRequest represents a chat completion request to OpenRouter
type OpenRouterRequest struct {
	Model       string              `json:"model"`
	Messages    []OpenRouterMessage `json:"messages"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature float64             `json:"temperature,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
}

// OpenRouterMessage represents a single message in the conversation
type OpenRouterMessage struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// OpenRouterResponse represents the API response from OpenRouter
type OpenRouterResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// OpenRouterModelsResponse represents the models API response
type OpenRouterModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
		ContextLength int `json:"context_length"`
		Architecture  struct {
			TokenizerConfig string `json:"tokenizer_config"`
			InstructType    string `json:"instruct_type"`
		} `json:"architecture"`
		TopProvider struct {
			MaxCompletionTokens int `json:"max_completion_tokens"`
		} `json:"top_provider"`
	} `json:"data"`
}

// NewOpenRouterClient creates a new OpenRouter client
func NewOpenRouterClient(apiKey string) *OpenRouterClient {
	return &OpenRouterClient{
		APIKey:  apiKey,
		BaseURL: "https://openrouter.ai/api/v1",
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// GetCompletion sends a chat completion request to OpenRouter
func (c *OpenRouterClient) GetCompletion(ctx context.Context, req OpenRouterRequest) (*OpenRouterResponse, error) {
	// Clean model ID - remove :free suffix if present
	req.Model = strings.TrimSuffix(req.Model, ":free")
	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// OpenRouter required headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("HTTP-Referer", "https://elchi.io")
	httpReq.Header.Set("X-Title", "Elchi Proxy Management Platform")
	httpReq.Header.Set("User-Agent", "Elchi-Backend/1.0")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		// Read the error response body for detailed debugging info
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyString := string(bodyBytes)

		var errorResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}

		// Try to parse structured error
		if err := json.Unmarshal(bodyBytes, &errorResp); err == nil && errorResp.Error.Message != "" {
			return nil, &OpenRouterError{
				StatusCode: resp.StatusCode,
				Message:    errorResp.Error.Message,
				Model:      req.Model,
				Type:       errorResp.Error.Type,
				Code:       errorResp.Error.Code,
				RawBody:    bodyString,
			}
		}

		// Fallback for non-structured error
		return nil, &OpenRouterError{
			StatusCode: resp.StatusCode,
			Message:    "API request failed",
			Model:      req.Model,
			RawBody:    bodyString,
		}
	}

	var response OpenRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("empty response choices")
	}

	return &response, nil
}

// GetModels fetches available models from OpenRouter API
func (c *OpenRouterClient) GetModels(ctx context.Context) ([]ModelInfo, error) {
	if len(c.models) > 0 {
		return c.models, nil // Return cached models
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("HTTP-Referer", "https://elchi.cloudnativeworks.com")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var response OpenRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Convert to our ModelInfo format
	models := make([]ModelInfo, 0, len(response.Data))
	for _, model := range response.Data {
		modelInfo := ModelInfo{
			ID:              model.ID,
			Name:            model.Name,
			ContextLength:   model.ContextLength,
			PromptPrice:     model.Pricing.Prompt,
			CompletionPrice: model.Pricing.Completion,
			SupportsSystem:  model.Architecture.InstructType != "",
			IsFree:          model.Pricing.Prompt == "0" && model.Pricing.Completion == "0",
		}

		models = append(models, modelInfo)
	}

	c.models = models
	return models, nil
}

