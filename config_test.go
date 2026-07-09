package confish

import (
	"context"
	"net/http"
	"testing"
)

func TestConfigFetchDecodesIntoStruct(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"site_name":"My App","max_upload_mb":25,"maintenance_mode":false}`))
	})
	c := newTestClient(t, srv.URL)

	type Config struct {
		SiteName        string `json:"site_name"`
		MaxUploadMB     int    `json:"max_upload_mb"`
		MaintenanceMode bool   `json:"maintenance_mode"`
	}
	var got Config
	if err := c.Config.Fetch(context.Background(), &got); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.SiteName != "My App" || got.MaxUploadMB != 25 || got.MaintenanceMode {
		t.Fatalf("unexpected config: %+v", got)
	}
	if (*calls)[0].Path != "/c/env_test" {
		t.Fatalf("path: %q", (*calls)[0].Path)
	}
	if (*calls)[0].Auth != "Bearer confish_sk_test" {
		t.Fatalf("auth: %q", (*calls)[0].Auth)
	}
}

func TestConfigUpdateWrapsValues(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	c := newTestClient(t, srv.URL)

	err := c.Config.Update(context.Background(), map[string]any{"maintenance_mode": true}, nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if (*calls)[0].Method != http.MethodPatch {
		t.Fatalf("method: %s", (*calls)[0].Method)
	}
	values, ok := (*calls)[0].Body["values"].(map[string]any)
	if !ok || values["maintenance_mode"] != true {
		t.Fatalf("body: %+v", (*calls)[0].Body)
	}
}

func TestConfigReplaceUsesPut(t *testing.T) {
	srv, calls := newTestServer(t, func(_ recordedRequest, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	c := newTestClient(t, srv.URL)

	err := c.Config.Replace(context.Background(), map[string]any{"site_name": "My App"}, nil)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if (*calls)[0].Method != http.MethodPut {
		t.Fatalf("method: %s", (*calls)[0].Method)
	}
	values, ok := (*calls)[0].Body["values"].(map[string]any)
	if !ok || values["site_name"] != "My App" {
		t.Fatalf("body: %+v", (*calls)[0].Body)
	}
}
