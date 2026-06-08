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
	flag.Parse()

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
	app := server.New(client, cfg.EffectiveRootFolderID())
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("gdrive-serve-lite listening on http://%s (config=%s root=%s metadataCacheTTL=%s)", *addr, cfg.Source, cfg.EffectiveRootFolderID(), metadataCacheTTL.String())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
