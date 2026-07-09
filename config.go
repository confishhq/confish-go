package confish

import (
	"context"
	"net/http"
)

// Config wraps the typed configuration endpoints.
type Config struct {
	client *Client
}

// Fetch retrieves the environment's typed configuration and decodes it into out.
// out must be a pointer to a struct (or map[string]any) with json tags matching
// the field keys defined in the application schema.
func (c *Config) Fetch(ctx context.Context, out any) error {
	return c.client.do(ctx, http.MethodGet, "/c/"+c.client.envID, nil, out)
}

// Update partially updates the environment's configuration values (PATCH).
// Only the fields present in values are changed. If out is non-nil, the response
// (the full updated configuration) is decoded into it.
func (c *Config) Update(ctx context.Context, values any, out any) error {
	return c.client.do(ctx, http.MethodPatch, "/c/"+c.client.envID, map[string]any{"values": values}, out)
}

// Replace replaces all configuration values (PUT). Fields not present in values are
// reset to their defaults. If out is non-nil, the full updated configuration is
// decoded into it.
func (c *Config) Replace(ctx context.Context, values any, out any) error {
	return c.client.do(ctx, http.MethodPut, "/c/"+c.client.envID, map[string]any{"values": values}, out)
}
