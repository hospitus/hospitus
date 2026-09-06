package client

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
)

// ExportOptions contains options for export operations
type ExportOptions struct {
	ExportPath       string `json:"export_path"`
	Compress         bool   `json:"compress"`
	StopInstance     bool   `json:"stop_instance"`
	IncludeSnapshots bool   `json:"include_snapshots"`
}

// ExportResult contains the result of an export operation
type ExportResult struct {
	Instance   string `json:"instance"`
	ExportPath string `json:"export_path"`
	Compress   bool   `json:"compress"`
	Message    string `json:"message"`
}

// ExportInstance exports an instance to a tarball
func (c *Client) ExportInstance(ctx context.Context, instanceID string, opts ExportOptions) (*ExportResult, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/export", url.PathEscape(instanceID))

	var result ExportResult
	// Export can take a long time, use extended timeout
	if err := c.doRequestWithTimeout(ctx, "POST", path, opts, &result, 10*time.Minute); err != nil {
		return nil, err
	}

	return &result, nil
}

// ImportOptions contains options for import operations
type ImportOptions struct {
	ImportPath       string `json:"import_path"`
	Provider         string `json:"provider"`
	NewName          string `json:"new_name,omitempty"`
	ResetMAC         bool   `json:"reset_mac"`
	NewIP            string `json:"new_ip,omitempty"`
	StartAfterImport bool   `json:"start_after_import"`
}

// ImportResult contains the result of an import operation
type ImportResult struct {
	Instance   *datastore.Instance `json:"instance"`
	ImportPath string              `json:"import_path"`
	Message    string              `json:"message"`
}

// ImportInstance imports an instance from a tarball
func (c *Client) ImportInstance(ctx context.Context, opts ImportOptions) (*ImportResult, error) {
	path := "/api/v1/import"

	var result ImportResult
	// Import can take a long time, use extended timeout
	if err := c.doRequestWithTimeout(ctx, "POST", path, opts, &result, 10*time.Minute); err != nil {
		return nil, err
	}

	return &result, nil
}
