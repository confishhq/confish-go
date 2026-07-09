// Package confish is the official Go SDK for confish (https://confi.sh).
//
// It provides a typed client for fetching configuration, sending logs,
// consuming actions, publishing feed items, and verifying webhook signatures.
//
// # Quick start
//
//	client, err := confish.New(confish.Options{
//	    EnvID:  os.Getenv("CONFISH_ENV_ID"),
//	    APIKey: os.Getenv("CONFISH_API_KEY"),
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	type Config struct {
//	    SiteName        string `json:"site_name"`
//	    MaxUploadMB     int    `json:"max_upload_mb"`
//	    MaintenanceMode bool   `json:"maintenance_mode"`
//	}
//
//	var cfg Config
//	if err := client.Config.Fetch(ctx, &cfg); err != nil {
//	    log.Fatal(err)
//	}
package confish
