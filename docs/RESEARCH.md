# Research Notes

## Objective

Build a Google Drive-specific HTTP server that covers the useful read-only parts of `rclone serve http`, but avoids rclone's generic VFS listing behavior for huge Drive folders.

## Primary sources checked

- Google Drive `files.list` REST reference: https://developers.google.com/workspace/drive/api/reference/rest/v3/files/list
- Google Drive search query guide: https://developers.google.com/workspace/drive/api/guides/search-files
- Google Drive download/export guide: https://developers.google.cn/workspace/drive/api/guides/manage-downloads?hl=en
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

The official `files.list` docs currently say page results can be partial or empty before the end, and callers must keep using `nextPageToken`. They also document a low maximum page size for shared drives as of the 2026-04-24 page. Empirically, the raw Drive API and rclone both returned 1000 files in a single request for the target shared-drive folder. The implementation should therefore:

- accept up to 1000 requested items because that matches observed Drive behavior and rclone usage
- never assume a full page was returned
- always return `nextPageToken`
- let callers keep paging

### Sorting

Drive supports `orderBy`, but the docs warn that `createdTime` can be problematic for large collections and recommend `modifiedTime desc` for time-related sorting on large item collections. In local timing, unsorted listing was faster than sorted listing.

Implication: default to unsorted unless the caller asks for order. The API should pass through limited sort options later rather than forcing a global sort.

### Path resolution

Drive allows duplicate names in the same parent folder. Paths are therefore a convenience layer, not a reliable identity. The server should resolve each segment by querying:

```text
trashed=false and '<parent-id>' in parents and name = '<segment>' and mimeType = 'application/vnd.google-apps.folder'
```

If a segment has more than one match, return a conflict and tell the caller to use `folderId`.

### Downloads and range requests

Drive blob downloads use:

```text
GET https://www.googleapis.com/drive/v3/files/{fileId}?alt=media
```

The Drive download guide documents partial downloads via the HTTP `Range` header. That is essential for video playback and seeking. Google Workspace document export is separate and does not support partial downloads, so v0 should only stream blob files.

### OAuth

Access tokens go in the `Authorization: Bearer ...` header. Refresh tokens can be used with Google's token endpoint to obtain new access tokens. Since this tool is initially a local read-only server, it can reuse an existing rclone remote's OAuth material or read credentials from environment variables.

Do not write refreshed tokens back to rclone config in v0. Keep refresh state in memory.

### rclone comparison

`rclone serve http` uses the VFS layer and exposes broad filesystem-like behavior. That brings directory caching, file buffering, chunked reading, optional VFS file cache, auth options, templates, filters, and more. This project should not try to copy all of that at once. The first useful slice is:

- ID-first listing
- JSON API
- basic HTML browser
- file streaming with Range forwarding

## Local timings that motivated this

Against `gdrive-ro:/teslacam1/RecentClips`:

| Operation | Result |
|---|---:|
| `rclone -vv lsd ...` | timed out at 60s, 0 rows |
| `rclone lsd ...` | timed out at 60s, 0 rows |
| `rclone lsf --files-only ...` | timed out at 60s, 0 rows |
| Drive backend query, folders only by real folder ID | about 1s |
| Direct Drive API, first 1000 files | about 1.6s |

## Initial API shape

```text
GET /api/list?folderId=...&type=all|files|dirs&pageSize=1000&pageToken=...&orderBy=...
GET /api/resolve?path=/a/b/c
GET /file/{fileId}/{optional-name}
GET /browse/{path}
GET /healthz
```

## Backlog

- local HTTP basic auth
- path and directory metadata cache with TTL
- export support for Google Workspace docs
- thumbnail endpoint
- stricter sort/filter allowlist
- file metadata endpoint
- persistent token store independent of rclone
- benchmark command that compares rclone and native Drive calls
- optional compatibility mode for rclone-like directory templates

