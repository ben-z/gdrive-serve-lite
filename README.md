# gdrive-serve-lite

`gdrive-serve-lite` is a small read-only HTTP server for Google Drive. It is meant to cover the useful parts of `rclone serve http` while staying optimized for very large Drive folders.

The first version is intentionally narrow:

- paginated Drive-native directory listing
- folder path resolution as a convenience
- file download/streaming by Drive file ID
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
  --remote gdrive-ro \
  --config ~/.config/rclone/rclone.conf \
  --root-folder-id 1-jJX3Mj7V6Afs-Elmr3g6AgwFo5KLgCM \
  --addr 127.0.0.1:8765
```

The `--config` flag is optional. If omitted, the server checks `RCLONE_CONFIG`, then the usual rclone config locations.

If your rclone config is encrypted, provide `RCLONE_CONFIG_PASS` or use the environment variables below:

```sh
RCLONE_CONFIG_PASS='your config password' ./gdrive-serve-lite --remote gdrive-ro
```

The server decrypts rclone's `RCLONE_ENCRYPT_V0` config format directly. It only reads the rclone config; refreshed access tokens are kept in memory and are not written back.

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
curl 'http://127.0.0.1:8765/api/list?folderId=1-jJX3Mj7V6Afs-Elmr3g6AgwFo5KLgCM&type=files&pageSize=1000&limit=1000'
```

`pageSize` is the requested Drive page size. `limit` is the maximum number of entries to return to the caller. If Drive returns a short partial page, the server follows `nextPageToken` until it has `limit` entries or reaches the end. Responses include `nextPageToken` and a ready-to-call `next` URL when more entries are available.

Resolve a folder path:

```sh
curl 'http://127.0.0.1:8765/api/resolve?path=/teslacam1/RecentClips'
```

Fetch file metadata:

```sh
curl 'http://127.0.0.1:8765/api/file?id=DRIVE_FILE_ID'
```

Browse in a browser:

```text
http://127.0.0.1:8765/browse/
```

Download or stream a file:

```text
http://127.0.0.1:8765/file/DRIVE_FILE_ID/name.mp4
```

`GET /file/...` forwards directly to Drive `alt=media` and preserves `Range`, so media seeking is a single upstream Drive request. `HEAD /file/...` uses one metadata request and returns length/type headers without streaming file bytes.

## Current limits

- read-only
- Google Drive only
- no directory cache yet
- no auth on the local HTTP server yet
- no Google Workspace document export yet
- duplicate folder names make path resolution ambiguous; use `folderId` directly when that happens
- rclone config password input is environment-only for now; no interactive prompt
