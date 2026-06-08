# gdrive-serve-lite

`gdrive-serve-lite` is a small read-only HTTP server for Google Drive. It is meant to cover the useful parts of `rclone serve http` while staying optimized for very large Drive folders.

The first version is intentionally narrow:

- paginated Drive-native directory listing
- folder and file path resolution as a convenience
- file download/streaming by Drive file ID
- optional file download/streaming by Drive path
- Google Workspace document export
- HTTP `Range` forwarding for media seeking
- optional reuse of an rclone remote's OAuth config

## Why

`rclone serve http` uses rclone's VFS abstraction. That is useful and general, but huge Google Drive folders can make path listing expensive before the server can return a small page. Google Drive's native `files.list` endpoint is already paginated and can filter by parent folder ID, MIME type, page token, and selected fields. This project exposes that narrower model directly.

## Build

```sh
go build ./cmd/gdrive-serve-lite
```

## Run with an rclone remote

```sh
./gdrive-serve-lite \
  --remote drive-ro:/Videos/Clips \
  --config ~/.config/rclone/rclone.conf \
  --metadata-cache-ttl 5m \
  --list-cache-ttl 30s \
  --user test-user \
  --pass '<http-password>' \
  --addr 127.0.0.1:8765
```

The `--config` flag is optional. If omitted, the server checks `RCLONE_CONFIG`, then the usual rclone config locations.

The `--remote` flag accepts either a remote name or the familiar rclone `remote:path` form. The path portion is resolved once at startup and used as the served root. You can also pass `--root-path /path/from/drive/root`; `--root-folder-id` is the fastest and most precise root selector when you already know the Drive folder ID.

If your rclone config is encrypted, provide `RCLONE_CONFIG_PASS` or use the environment variables below:

```sh
RCLONE_CONFIG_PASS='<rclone-config-password>' ./gdrive-serve-lite --remote drive-ro
```

The server decrypts rclone's `RCLONE_ENCRYPT_V0` config format directly. It only reads the rclone config; refreshed access tokens are kept in memory and are not written back.

The `--metadata-cache-ttl` flag controls a small positive lookup cache for file metadata and path segment resolution. The default is `5m`, which keeps repeated `/path/...`, `/export/...`, and `HEAD` requests fast.

The `--list-cache-ttl` flag controls an exact-query cache for successful directory/list responses. The default is `30s`. The cache key includes folder ID, page token, type, order, page size, and limit, so different pages and filters do not collide. Set either cache TTL to `0` to disable it.

Use `--user` and `--pass` together to require HTTP Basic Auth on every endpoint. The default is no local HTTP auth, matching the usual local-only workflow.

## Run without rclone

```sh
export GDSL_CLIENT_ID='...'
export GDSL_CLIENT_SECRET='...'
export GDSL_REFRESH_TOKEN='...'
export GDSL_DRIVE_ID='...'          # optional shared drive ID
export GDSL_ROOT_FOLDER_ID='root'   # optional; defaults to drive ID, then root

./gdrive-serve-lite --addr 127.0.0.1:8765
```

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

`pageSize` is the requested Drive page size. `limit` is the maximum number of entries to return to the caller. If Drive returns a short partial page, the server follows `nextPageToken` until it has `limit` entries or reaches the end. Responses include `nextPageToken` and a ready-to-call `next` URL when more entries are available.

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

Download or stream a file:

```text
http://127.0.0.1:8765/file/DRIVE_FILE_ID/name.mp4
http://127.0.0.1:8765/file/DRIVE_FILE_ID/name.mp4?resourceKey=RESOURCE_KEY
```

`GET /file/...` forwards directly to Drive `alt=media` and preserves `Range`, so media seeking is a single upstream Drive request. `HEAD /file/...` uses one metadata request and returns length/type headers without streaming file bytes.

If Drive returns a `resourceKey` or `shortcutDetails.targetResourceKey` in metadata, path-based reads and generated browser links automatically pass it through on the upstream Drive request. ID-based endpoints accept `resourceKey=...` explicitly.

Export a Google Docs/Sheets/Slides file:

```text
http://127.0.0.1:8765/export/GOOGLE_DOC_FILE_ID/name.docx
http://127.0.0.1:8765/export/GOOGLE_DOC_FILE_ID/name.pdf?format=pdf
```

If no `format` or `mimeType` is provided, the server defaults to rclone-like export formats: Docs as `docx`, Sheets as `xlsx`, Slides as `pptx`, Drawings as `svg`, Apps Script as `json`, and Google Vids as `mp4`. Drive exports do not support HTTP range reads, so `Range` requests are rejected for `/export/...`.

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
