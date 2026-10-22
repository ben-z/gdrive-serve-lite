# gdrive-serve-lite

`gdrive-serve-lite` is a small read-only HTTP server for Google Drive. It lists and streams large Drive folders directly through the Drive API without rclone's VFS layer.

## Features

- paginated Drive-native directory listing
- folder and file path resolution
- file download/streaming by Drive file ID or path
- Google Workspace document export
- HTTP `Range` forwarding for media seeking
- optional rclone OAuth config reuse
- optional Basic Auth, CORS, and reverse-proxy path prefix

## Why

`rclone serve http` is general-purpose. For huge Google Drive folders, that can make a small listing slow. This project maps directly to Drive's paginated `files.list` API and keeps the HTTP surface narrow.

## Build

```sh
go build ./cmd/gdrive-serve-lite
```

## Run with an rclone remote

```sh
./gdrive-serve-lite --remote drive-ro:/Videos --addr 127.0.0.1:8765
```

The `--config` flag is optional and requires `--remote`. If omitted, the server checks `RCLONE_CONFIG`, then the usual rclone config locations. A selected config file must be readable and valid; errors stop startup. When `--remote` is provided, that explicit rclone remote is used even if `GDSL_*` environment variables are also present. Environment auth is used when no remote is requested.

The `--remote` flag accepts either a Google Drive remote name or the familiar rclone `remote:path` form. Non-Drive rclone remotes are rejected at startup. The path portion is resolved once at startup and used as the served root. You can also pass `--root-path /path/from/drive/root`; `--root-folder-id` is the fastest and most precise root selector when you already know the Drive folder ID. Use `--root-resource-key` with `--root-folder-id` for a link-shared root folder that requires a Drive resource key. Changing the root folder or implicit shared-drive root clears its inherited resource key; supply the new root's key explicitly.

CLI root paths contain literal Drive names. For example, `--remote 'drive-ro:/50% off'` selects the folder named `50% off`; percent signs remain part of the name. Leading and trailing spaces in path segments are preserved; quote the argument in your shell.

Use `--drive-id` to target a shared drive. It scopes listing and search to that drive and, when no explicit root is given, serves the drive's root. It overrides the remote's `team_drive` setting and the `GDSL_DRIVE_ID` environment variable.

If your rclone config is encrypted, provide `RCLONE_CONFIG_PASS` or use the environment variables below:

```sh
RCLONE_CONFIG_PASS='<rclone-config-password>' ./gdrive-serve-lite --remote drive-ro
```

The server decrypts rclone's `RCLONE_ENCRYPT_V0` config format directly. It only reads the rclone config; refreshed access tokens are kept in memory and are not written back.

The `--metadata-cache-ttl` flag controls a small positive lookup cache for file metadata and path segment resolution. The default is `5m`, which keeps repeated `/path/...`, `/export/...`, and `HEAD` requests fast.

The `--list-cache-ttl` flag controls an exact-query cache for successful directory/list responses. The default is `30s`. The cache key includes folder ID, page token, type, order, page size, and limit, so different pages and filters do not collide. Set either cache TTL to `0` to disable it.

Expired cache entries are pruned during subsequent writes. Cache TTLs and the response-header timeout must be non-negative; `0` disables the corresponding setting.

The `--drive-response-header-timeout` flag bounds how long the server waits for Google API response headers. The default is `30s`; it does not cap the duration of a long media response body after Drive starts sending it.

Use `--user` and `--pass` together to require HTTP Basic Auth on every endpoint. The default is no local HTTP auth, matching the usual local-only workflow.

Use `--allow-origin` to enable CORS for browser tools. Pass a specific origin such as `http://localhost:3000` when using credentials, or `*` for unauthenticated local experiments.

Use `--baseurl` to serve under a path prefix, matching rclone's reverse-proxy-friendly behavior. For example, `--baseurl /gdrive` serves `/api/list` at `/gdrive/api/list` and generated JSON/HTML links include `/gdrive`.

## Run without rclone

```sh
export GDSL_CLIENT_ID='...'
export GDSL_CLIENT_SECRET='...'
export GDSL_REFRESH_TOKEN='...'
export GDSL_DRIVE_ID='...'          # optional shared drive ID
export GDSL_ROOT_FOLDER_ID='root'   # optional; defaults to drive ID, then root
export GDSL_ROOT_RESOURCE_KEY='...' # optional link-shared root resource key

./gdrive-serve-lite --addr 127.0.0.1:8765
```

For an existing access token, set `GDSL_ACCESS_TOKEN` and optionally `GDSL_TOKEN_EXPIRY` to its RFC 3339 expiry. An invalid expiry stops startup. Refresh tokens require `GDSL_CLIENT_ID`; access tokens alone are also supported. Access tokens with a known expiry must still be valid unless a refresh token is configured.

## API

List a folder by ID:

```sh
curl 'http://127.0.0.1:8765/api/list?folderId=DRIVE_FOLDER_ID&type=files&pageSize=1000&limit=1000'
```

For link-shared folders that require a Drive resource key:

```sh
curl 'http://127.0.0.1:8765/api/list?folderId=DRIVE_FOLDER_ID&resourceKey=RESOURCE_KEY&type=files&pageSize=1000&limit=1000'
```

If `folderId` and `path` are omitted, `/api/list` lists the configured root folder:

```sh
curl 'http://127.0.0.1:8765/api/list?type=files&pageSize=1000&limit=1000'
```

`pageSize` is the requested Drive page size. `limit` is the maximum number of entries to return to the caller. If Drive returns a short partial page, the server follows `nextPageToken` until it has `limit` entries or reaches the end. Responses include `nextPageToken` and a ready-to-call `next` URL when more entries are available. Pass `links=1` to include a per-entry server `url` for the low-latency read/export route, or the folder browse route. Folder shortcuts are treated as folders for `type=folders` and excluded from `type=files`.

`pageSize` and `limit` must be integers when provided. Malformed values return HTTP 400. Path resolution also follows partial Drive pages and requires a complete search to establish a unique match; duplicate names return HTTP 409.

Resolve a folder path:

```sh
curl 'http://127.0.0.1:8765/api/resolve?path=/Videos/Clips'
```

Resolve a file path:

```sh
curl 'http://127.0.0.1:8765/api/resolve?path=/Videos/Clips/clip.mp4&type=file'
```

`type` can be `folder`, `file`, or `any`. The default is `folder` for compatibility with folder listing and browsing.

Fetch file metadata:

```sh
curl 'http://127.0.0.1:8765/api/file?id=DRIVE_FILE_ID'
curl 'http://127.0.0.1:8765/api/file?id=DRIVE_FILE_ID&resourceKey=RESOURCE_KEY'
```

Fetch supported export formats for a Google Workspace file:

```sh
curl 'http://127.0.0.1:8765/api/export-formats?id=GOOGLE_DOC_FILE_ID'
```

Browse in a browser:

```text
http://127.0.0.1:8765/
http://127.0.0.1:8765/browse/
```

Folder paths can also return the same paginated JSON listing shape as `/api/list`:

```sh
curl 'http://127.0.0.1:8765/?format=json&type=files&pageSize=1000&limit=1000'
curl -H 'Accept: application/json' 'http://127.0.0.1:8765/Videos/Clips?type=files&pageSize=1000&limit=1000'
```

JSON media ranges in `Accept` must have a positive quality value to select JSON. Malformed media ranges or quality values return HTTP 400; explicit JSON query options take precedence.

`HEAD` on folder paths and `/browse/...` resolves the folder and returns the listing content type without fetching a Drive page.

Download or stream a file:

```text
http://127.0.0.1:8765/file/DRIVE_FILE_ID/name.mp4
http://127.0.0.1:8765/file/DRIVE_FILE_ID/name.mp4?resourceKey=RESOURCE_KEY
```

`GET /file/...` forwards directly to Drive `alt=media` and preserves `Range` plus standard conditional read headers such as `If-None-Match`, `If-Modified-Since`, and `If-Range`, so media seeking and cache validation are single upstream Drive requests. Media read status codes and headers, including `Content-Disposition`, cache headers, checksum headers such as `X-Goog-Hash`, and range errors like `416` with `Content-Range`, are passed through. `HEAD /file/...` uses one metadata request and returns length/type/modified-time headers without streaming file bytes. Blob `HEAD` responses advertise `Accept-Ranges: bytes` and honor `If-Modified-Since` with `304 Not Modified` when Drive metadata proves the file is fresh.

If Drive interrupts a download or export, the server aborts the response so clients can detect the incomplete transfer.

File and export `HEAD` responses use known modification times to evaluate date preconditions. A stale `If-Unmodified-Since` returns `412 Precondition Failed` before checking `If-Modified-Since`. Requests containing `If-Match` skip the `If-Unmodified-Since` check. Metadata-based `HEAD` responses do not evaluate ETag preconditions, and export `GET` requests do not forward conditional request headers to Drive.

If Drive returns a `resourceKey` or `shortcutDetails.targetResourceKey` in metadata, path-based reads, multi-segment path resolution, and generated browser links automatically pass it through on the upstream Drive request. ID-based endpoints accept `resourceKey=...` explicitly.

For shortcut path reads and exports, `HEAD` describes the target's metadata while retaining the shortcut's download name.

Export a Google Docs/Sheets/Slides file:

```text
http://127.0.0.1:8765/export/GOOGLE_DOC_FILE_ID/name.docx
http://127.0.0.1:8765/export/GOOGLE_DOC_FILE_ID/name.pdf?format=pdf
```

If no `format` or `mimeType` is provided, the server defaults to rclone-like export formats: Docs as `docx`, Sheets as `xlsx`, Slides as `pptx`, Drawings as `svg`, Apps Script as `json`, and Google Vids as `mp4`. Explicit export formats are checked against Drive metadata when available, then Drive export responses are passed through with Drive's status, body, `Content-Disposition`, cache headers, and other read headers. Drive exports do not support HTTP range reads, so `Range` requests are rejected locally for `/export/...`. Export `HEAD` responses use the source file's Drive `modifiedTime` for `Last-Modified` and `If-Modified-Since`.

You can also stream by path:

```text
http://127.0.0.1:8765/Videos/Clips/name.mp4
http://127.0.0.1:8765/path/Videos/Clips/name.mp4
http://127.0.0.1:8765/Documents/Meeting%20Notes?format=pdf
http://127.0.0.1:8765/path/Documents/Meeting%20Notes?format=pdf
```

Plain root-relative paths are the rclone-like compatibility route: folders render an HTML listing, blob files stream from Drive, and Google Workspace files export when needed. `/path/...` is the same path-based file read behavior under an explicit prefix. Path reads resolve each segment first, so `/file/DRIVE_FILE_ID/...` is the lower-latency path for blob files when you already have the file ID from `/api/list`.

## Current limits

- read-only
- Google Drive only
- duplicate names make path resolution ambiguous; use `folderId` or file ID directly when that happens
- root-relative paths under reserved prefixes such as `/api`, `/file`, `/path`, `/export`, `/browse`, and `/healthz` are treated as server endpoints
- rclone config password input is environment-only for now; no interactive prompt

## Development

```sh
go test -race -count=1 ./...
go vet ./...
go build ./...
GOTOOLCHAIN="$(go env GOVERSION)" go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

GitHub Actions runs these checks on Linux and macOS, verifies module checksums, and checks Go formatting. The Go version is selected from `go.mod`.
