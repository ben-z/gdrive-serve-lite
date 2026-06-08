package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"gdrive-serve-lite/internal/config"
	"gdrive-serve-lite/internal/drive"
	"gdrive-serve-lite/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "HTTP listen address")
	remote := flag.String("remote", "", "rclone remote name to read OAuth config from")
	configPath := flag.String("config", "", "rclone config file path")
	rootFolderID := flag.String("root-folder-id", "", "Drive folder ID to use as browse/resolve root")
	driveID := flag.String("drive-id", "", "shared drive ID override")
	metadataCacheTTL := flag.Duration("metadata-cache-ttl", drive.DefaultMetadataCacheTTL, "positive metadata/path lookup cache TTL; 0 disables")
	listCacheTTL := flag.Duration("list-cache-ttl", drive.DefaultListCacheTTL, "exact directory/list response cache TTL; 0 disables")
	httpUser := flag.String("user", "", "HTTP basic auth username; requires --pass")
	httpPass := flag.String("pass", "", "HTTP basic auth password; requires --user")
	flag.Parse()
	if (*httpUser == "") != (*httpPass == "") {
		log.Fatal("--user and --pass must be set together")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := config.Load(ctx, config.LoadOptions{
		Remote:     *remote,
		ConfigPath: *configPath,
	})
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *rootFolderID != "" {
		cfg.RootFolderID = *rootFolderID
	}
	if *driveID != "" {
		cfg.DriveID = *driveID
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	client := drive.NewClient(cfg)
	client.SetMetadataCacheTTL(*metadataCacheTTL)
	client.SetListCacheTTL(*listCacheTTL)
	app := server.New(client, cfg.EffectiveRootFolderID())
	app.SetBasicAuth(*httpUser, *httpPass)
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("gdrive-serve-lite listening on http://%s (config=%s root=%s metadataCacheTTL=%s listCacheTTL=%s)", *addr, cfg.Source, cfg.EffectiveRootFolderID(), metadataCacheTTL.String(), listCacheTTL.String())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
