package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/CloudNativeWorks/elchi-backend/pkg/models"
)

// EnvoyVersionInfo represents the structure from archive.elchi.io/index.json
type EnvoyVersionInfo struct {
	Version     string `json:"version"`
	ReleaseDate string `json:"release_date"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// AvailableVersionsResponse represents the response structure
type AvailableVersionsResponse struct {
	Versions    []EnvoyVersionInfo `json:"versions"`
	LastUpdated string             `json:"last_updated"`
	Source      string             `json:"source"`
}

func (custom *AppHandler) GetAvailableVersions(ctx context.Context, _ models.ResourceClass, requestDetails models.RequestDetails) (any, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Make request to archive.elchi.io
	resp, err := client.Get("https://archive.elchi.io/index.json")
	if err != nil {
		custom.Logger.Error("Failed to fetch available versions", "error", err)
		return nil, fmt.Errorf("failed to fetch available versions: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		custom.Logger.Error("Invalid response status", "status_code", resp.StatusCode)
		return nil, fmt.Errorf("invalid response from archive server: status %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		custom.Logger.Error("Failed to read response body", "error", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse JSON response
	var archiveData any
	if err := json.Unmarshal(body, &archiveData); err != nil {
		custom.Logger.Error("Failed to parse JSON response", "error", err)
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Log successful fetch
	custom.Logger.Info("Successfully fetched available versions",
		"client_id", requestDetails.ClientID,
		"project", requestDetails.Project,
		"response_size", len(body))

	// Return the raw data from archive.elchi.io
	return archiveData, nil
}
