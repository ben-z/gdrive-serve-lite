package main

import (
	"cmp"
	"context"
	"flag"
	"log"
	"net/http"
	"path"
	"strings"
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
	rootResourceKey := flag.String("root-resource-key", "", "Drive resource key for --root-folder-id")
	rootPath := flag.String("root-path", "", "Drive folder path to use as browse/resolve root")
	driveID := flag.String("drive-id", "", "shared drive ID override")
	metadataCacheTTL := flag.Duration("metadata-cache-ttl", drive.DefaultMetadataCacheTTL, "positive metadata/path lookup cache TTL; 0 disables")
	listCacheTTL := flag.Duration("list-cache-ttl", drive.DefaultListCacheTTL, "exact directory/list response cache TTL; 0 disables")
	driveResponseHeaderTimeout := flag.Duration("drive-response-header-timeout", 30*time.Second, "maximum wait for Google API response headers; 0 disables")
	httpUser := flag.String("user", "", "HTTP basic auth username; requires --pass")
	httpPass := flag.String("pass", "", "HTTP basic auth password; requires --user")
	allowOrigin := flag.String("allow-origin", "", "optional CORS origin to allow, for example http://localhost:3000 or *")
	baseURL := flag.String("baseurl", "", "optional URL path prefix to serve from, for example /gdrive")
	flag.Parse()
	if (*httpUser == "") != (*httpPass == "") {
		log.Fatal("--user and --pass must be set together")
	}
	for _, option := range []struct {
		name     string
		duration time.Duration
	}{
		{"metadata-cache-ttl", *metadataCacheTTL},
		{"list-cache-ttl", *listCacheTTL},
		{"drive-response-header-timeout", *driveResponseHeaderTimeout},
	} {
		if option.duration < 0 {
			log.Fatalf("--%s must be non-negative; use 0 to disable", option.name)
		}
	}
	remoteName, remoteRootPath := splitRemotePath(*remote)
	if *remote != "" && remoteName == "" {
		log.Fatal("--remote requires a remote name before the colon")
	}

	cfg, err := config.Load(remoteName, *configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *rootFolderID != "" {
		cfg.RootFolderID = *rootFolderID
	}
	if *rootResourceKey != "" {
		cfg.RootResourceKey = *rootResourceKey
	}
	if *driveID != "" {
		cfg.DriveID = *driveID
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	client := drive.NewClient(cfg, drive.NewHTTPClient(*driveResponseHeaderTimeout))
	client.SetMetadataCacheTTL(*metadataCacheTTL)
	client.SetListCacheTTL(*listCacheTTL)

	rootID, rootKey := cfg.EffectiveRootFolderID(), cfg.RootResourceKey
	if p := cmp.Or(*rootPath, remoteRootPath); *rootFolderID == "" && p != "" && p != "/" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		item, err := client.ResolveItemPath(ctx, p, rootID, rootKey, "folder")
		cancel()
		if err != nil {
			log.Fatalf("resolve root path %q: %v", p, err)
		}
		rootID, rootKey = item.File.TargetIDAndKey()
	}

	app := server.New(client, server.Options{
		RootFolderID:    rootID,
		RootResourceKey: rootKey,
		Username:        *httpUser,
		Password:        *httpPass,
		AllowOrigin:     *allowOrigin,
		BaseURL:         *baseURL,
	})
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("gdrive-serve-lite listening on http://%s (config=%s root=%s metadataCacheTTL=%s listCacheTTL=%s driveResponseHeaderTimeout=%s)", *addr, cfg.Source, rootID, metadataCacheTTL, listCacheTTL, driveResponseHeaderTimeout)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// splitRemotePath splits an rclone-style "remote:path" into the remote name
// and a cleaned, slash-prefixed root path.
func splitRemotePath(remote string) (name, root string) {
	name, root, ok := strings.Cut(strings.TrimSpace(remote), ":")
	if root = strings.TrimSpace(root); !ok || root == "" {
		return name, ""
	}
	return name, path.Clean("/" + root)
}
