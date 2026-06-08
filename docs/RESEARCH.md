# Research Notes

## Objective

Build a Google Drive-specific HTTP server that covers the useful read-only parts of `rclone serve http`, but avoids rclone's generic VFS listing behavior for huge Drive folders.

## Primary sources checked

- Google Drive `files.list` REST reference: https://developers.google.com/workspace/drive/api/reference/rest/v3/files/list
- Google Drive search query guide: https://developers.google.com/workspace/drive/api/guides/search-files
- Google Drive download/export guide: https://developers.google.cn/workspace/drive/api/guides/manage-downloads?hl=en
- Google Drive resource key guide: https://developers.google.com/workspace/drive/api/guides/resource-keys
- Google OAuth 2.0 overview and installed app docs: https://developers.google.com/identity/protocols/oauth2 and https://developers.google.com/identity/protocols/oauth2/native-app
- rclone `serve http` docs: https://rclone.org/commands/rclone_serve_http/

## Key findings

### Listing

Drive's `files.list` endpoint supports a `q` query, `pageToken`, `nextPageToken`, `fields`, `orderBy`, `supportsAllDrives`, `includeItemsFromAllDrives`, `driveId`, and `corpora=drive`.

The important query shape for direct children is:

```text
trashed=false and '<folder-id>' in parents
```

Drive folders are ordinary file resources with this MIME type:

```text
application/vnd.google-apps.folder
```

That gives efficient server-side filters:

```text
mimeType = 'application/vnd.google-apps.folder'
mimeType != 'application/vnd.google-apps.folder'
```

The official `files.list` docs currently say page results can be partial or empty before the end, and callers must keep using `nextPageToken`. They also document a low maximum page size for shared drives as of the 2026-04-24 page. Empirically, the raw Drive API and rclone both returned 1000 files in a single request for a large shared-drive folder. The implementation should therefore:

- accept up to 1000 requested items because that matches observed Drive behavior and rclone usage
- never assume a full page was returned
- always return `nextPageToken`
- let callers keep paging

Implementation detail: `/api/list` accepts both `pageSize` and `limit`. `pageSize` controls the Drive request size; `limit` controls how many entries the HTTP response should contain. This keeps normal one-page reads cheap while making "first 1000 files" ergonomic even if Drive returns short pages.

### Sorting

Drive supports `orderBy`, but the docs warn that `createdTime` can be problematic for large collections and recommend `modifiedTime desc` for time-related sorting on large item collections. In local timing, unsorted listing was faster than sorted listing.

Implication: default to unsorted unless the caller asks for order. The API should pass through limited sort options later rather than forcing a global sort.

### Path resolution

Drive allows duplicate names in the same parent folder. Paths are therefore a convenience layer, not a reliable identity. The server resolves intermediate segments as folders by querying:

```text
trashed=false and '<parent-id>' in parents and name = '<segment>' and mimeType = 'application/vnd.google-apps.folder'
```

For the leaf segment, `/api/resolve` can resolve `type=folder`, `type=file`, or `type=any`. The path streaming endpoint resolves the leaf as `type=file`. Each lookup asks for two matches so ambiguity can be detected without scanning all duplicates. If a segment has more than one match, return a conflict and tell the caller to use `folderId` or a file ID directly.

### Downloads and range requests

Drive blob downloads use:

```text
GET https://www.googleapis.com/drive/v3/files/{fileId}?alt=media
```

The Drive download guide documents partial downloads via the HTTP `Range` header. That is essential for video playback and seeking.

The media endpoint also forwards standard HTTP validator headers (`If-None-Match`, `If-Modified-Since`, `If-Match`, `If-Unmodified-Since`, and `If-Range`) to Drive. This keeps browser cache validation and range revalidation as one upstream Drive request while avoiding broad forwarding of unrelated local request headers.

Google Workspace document export is separate:

```text
GET https://www.googleapis.com/drive/v3/files/{fileId}/export?mimeType=...
```

Exports are all-at-once reads and are capped by Drive, so the server rejects `Range` for exports instead of pretending media seeking works there. Blob reads remain the fast path: `GET /file/{id}` still maps directly to one `alt=media` request.

Drive link-shared files can require resource keys. The Drive API returns them in `resourceKey`, and shortcut target keys in `shortcutDetails.targetResourceKey`. Requests that need them use the `X-Goog-Drive-Resource-Keys` header with `fileId/resourceKey` pairs. Path-based reads can use the key from metadata; ID-based reads accept `resourceKey=...`.

### OAuth

Access tokens go in the `Authorization: Bearer ...` header. Refresh tokens can be used with Google's token endpoint to obtain new access tokens. Since this tool is initially a local read-only server, it can reuse an existing rclone remote's OAuth material or read credentials from environment variables.

Do not write refreshed tokens back to rclone config in v0. Keep refresh state in memory.

### Rclone encrypted config files

Rclone's config encryption implementation lives in `fs/config/crypt.go`. The encrypted config starts with:

```text
RCLONE_ENCRYPT_V0:
```

The bytes after the header are base64. Decoded bytes are:

```text
24-byte nonce || nacl/secretbox ciphertext
```

The secretbox key is:

```text
SHA256("[" + NFKC(password) + "][rclone-config]")
```

Rclone rejects invalid UTF-8 passwords, warns about leading/trailing whitespace, normalizes with Unicode NFKC, and rejects empty or all-whitespace passwords. This project implements the same key derivation and decrypts encrypted rclone config files directly when `RCLONE_CONFIG_PASS` is set.

Validation experiment: a fake rclone config file was generated locally and encrypted with:

```sh
RCLONE_CONFIG_PASS=fixture-pass \
  rclone --config tmp/plain-rclone.conf \
  --password-command 'printf fixture-pass' \
  config encryption set
```

That rclone-generated encrypted fixture is covered by unit tests.

### rclone comparison

`rclone serve http` uses the VFS layer and exposes broad filesystem-like behavior. That brings directory caching, file buffering, chunked reading, optional VFS file cache, auth options, templates, filters, and more. This project should not try to copy all of that at once. The first useful slice is:

- ID-first listing
- JSON API
- basic HTML browser
- file streaming with Range forwarding
- metadata endpoint separated from media streaming, so GET reads stay one upstream request
- rclone-like root-relative folder and file paths, with file-ID streaming kept as the fastest path
- Workspace document export through Drive's `files.export`, kept separate from blob media reads
- positive metadata/path lookup cache with TTL
- exact-query directory/list response cache with TTL; cache keys include page token and pagination knobs
- optional HTTP Basic Auth with `--user` and `--pass`
- rclone-style `--remote remote:path` root selection, resolved once at startup
- Drive resource-key propagation for link-shared files and shortcut targets

## Local timings that motivated this

Against a large shared-drive folder:

| Operation | Result |
|---|---:|
| `rclone -vv lsd ...` | timed out at 60s, 0 rows |
| `rclone lsd ...` | timed out at 60s, 0 rows |
| `rclone lsf --files-only ...` | timed out at 60s, 0 rows |
| Drive backend query, folders only by real folder ID | about 1s |
| Direct Drive API, first 1000 files | about 1.6s |

## Initial API shape

```text
GET /api/list?folderId=...&type=all|files|dirs&pageSize=1000&limit=1000&pageToken=...&orderBy=...
GET /api/list?type=files&pageSize=1000&limit=1000
GET /api/file?id=...
GET /api/export-formats?id=...
GET /api/resolve?path=/a/b/c&type=folder|file|any
GET /file/{fileId}/{optional-name}
GET /export/{fileId}/{optional-name}?format=pdf
GET /path/{path-to-file}
GET /{path-to-file-or-folder}
GET /browse/{path}
GET /healthz
```

## Backlog

- thumbnail endpoint
- stricter sort/filter allowlist
- persistent token store independent of rclone
- benchmark command that compares rclone and native Drive calls
- optional compatibility mode for rclone-like directory templates
