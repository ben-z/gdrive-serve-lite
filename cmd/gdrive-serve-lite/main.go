package main

import (
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
	flag.Parse()
	if (*httpUser == "") != (*httpPass == "") {
		log.Fatal("--user and --pass must be set together")
	}
	remoteName, remoteRootPath := splitRemotePath(*remote)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := config.Load(ctx, config.LoadOptions{
		Remote:     remoteName,
		ConfigPath: *configPath,
	})
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

	client := drive.NewClientWithHTTPClient(cfg, driveHTTPClient(*driveResponseHeaderTimeout))
	client.SetMetadataCacheTTL(*metadataCacheTTL)
	client.SetListCacheTTL(*listCacheTTL)
	effectiveRootID := cfg.EffectiveRootFolderID()
	effectiveRootResourceKey := cfg.RootResourceKey
	if *rootFolderID == "" {
		selectedRootPath := *rootPath
		if selectedRootPath == "" {
			selectedRootPath = remoteRootPath
		}
		if selectedRootPath != "" && selectedRootPath != "/" {
			resolvedRoot, err := client.ResolveItemPathWithResourceKey(ctx, selectedRootPath, effectiveRootID, effectiveRootResourceKey, "folder")
			if err != nil {
				log.Fatalf("resolve root path %q: %v", selectedRootPath, err)
			}
			effectiveRootID, effectiveRootResourceKey = drive.ReadableFileIDAndResourceKey(resolvedRoot.File)
		}
	}
	app := server.NewWithRootResourceKey(client, effectiveRootID, effectiveRootResourceKey)
	app.SetBasicAuth(*httpUser, *httpPass)
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("gdrive-serve-lite listening on http://%s (config=%s root=%s metadataCacheTTL=%s listCacheTTL=%s driveResponseHeaderTimeout=%s)", *addr, cfg.Source, effectiveRootID, metadataCacheTTL.String(), listCacheTTL.String(), driveResponseHeaderTimeout.String())
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func driveHTTPClient(responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: transport}
}

func splitRemotePath(remote string) (string, string) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", ""
	}
	name, root, ok := strings.Cut(remote, ":")
	if !ok {
		return remote, ""
	}
	return name, cleanRootPath(root)
}

func cleanRootPath(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if !strings.HasPrefix(root, "/") {
		root = "/" + root
	}
	return path.Clean(root)
}
