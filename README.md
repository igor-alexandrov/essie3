# essie3

**A tiny, filesystem-backed S3-compatible server for local development and testing.**

[![Release](https://img.shields.io/github/v/release/igor-alexandrov/essie3?sort=semver)](https://github.com/igor-alexandrov/essie3/releases)
[![CI](https://github.com/igor-alexandrov/essie3/actions/workflows/ci.yml/badge.svg)](https://github.com/igor-alexandrov/essie3/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/igor-alexandrov/essie3)](go.mod)
[![License](https://img.shields.io/github/license/igor-alexandrov/essie3)](LICENSE)

essie3 speaks enough of the S3 REST API to stand in for AWS S3 when running
integration tests, demos, or offline dev environments. It's a single Go
binary with **zero third-party dependencies**.

When an object is missing, essie3 can return a deterministic **fallback
placeholder** instead of `404 NoSuchKey` — useful when seeding a dev
environment where the real assets don't exist yet, so your UI doesn't render
broken images.

> [!WARNING]
> essie3 is **not** a production S3 replacement — no SigV4 signature
> verification, no versioning, no real `ListObjects`. It exists to stand in
> for AWS S3 in local dev and integration tests. (Optional access-key auth is
> available for tests that need to exercise the auth-failure path; see
> [`ESSIE3_ACCESS_KEY`](#configuration).)

## Quick start

```sh
go run .                                   # listens on :9000

# In another shell:
curl -X PUT http://localhost:9000/mybucket # create a bucket
curl -X PUT --data-binary @photo.jpg \
  -H "Content-Type: image/jpeg" \
  http://localhost:9000/mybucket/photo.jpg # upload
curl http://localhost:9000/mybucket/photo.jpg -o out.jpg  # download
```

That's it. Point any S3 client at `http://localhost:9000` and go.

## Contents

- [Features](#features)
- [Running](#running)
- [Configuration](#configuration)
- [Usage](#usage)
- [Range requests](#range-requests)
- [Auth (optional)](#auth-optional)
- [Fallback placeholders](#fallback-placeholders)
- [Admin dashboard](#admin-dashboard)
- [Storage layout](#storage-layout)
- [Development](#development)
- [License](#license)

## Features

**S3 API**

- `PUT`, `GET`, `HEAD`, `DELETE`, `POST` (multipart), and `COPY` for objects
- Bucket create / head / list (stub)
- CORS enabled for browser uploads
- Per-object metadata persisted as JSON sidecar files
- HTTP `Range` requests (single-range) with `If-Range` ETag matching, on
  objects and fallback placeholders

**Fallbacks**

- Deterministic fallback placeholders by file extension
  (`.jpg` / `.jpeg` / `.png` / `.gif` / `.webp` / `.pdf` / `.mp4` / `.mov` /
  `.webm` / `.avi`)
- Optional on-demand bubble-identicon generator for PNG / JPEG keys

**Safety & ops**

- Atomic object writes (temp-file + rename)
- Path-traversal protection on bucket and key names
- Optional access-key auth with an `x-amz-acl: public-read` escape hatch
- Graceful shutdown on `SIGINT` / `SIGTERM`

## Running

### From source

```sh
go run .
```

### With Docker Compose

```yaml
services:
  essie3:
    image: igoraleksandrov/essie3:latest
    ports:
      - "9000:9000"
    volumes:
      - ./data:/data
      - ./fallback-data:/fallback-data
    environment:
      ESSIE3_DATA_DIR: /data
      ESSIE3_FALLBACK_DATA_DIR: /fallback-data
```

```sh
docker compose up
```

## Configuration

All configuration is via environment variables. The fallback-related options
have dedicated sections below with the full details.

| Variable                            | Default           | Description                                                                 |
| ----------------------------------- | ----------------- | --------------------------------------------------------------------------- |
| `ESSIE3_PORT`                       | `9000`            | HTTP port to listen on.                                                     |
| `ESSIE3_DATA_DIR`                   | `./data`          | Where uploaded objects are stored.                                          |
| `ESSIE3_FALLBACK_DATA_DIR`          | `./fallback-data` | Directory of curated fallback placeholders.                                 |
| `ESSIE3_FALLBACK_MODE`              | `prefer-pool`     | How missing objects are filled. See [Fallback placeholders](#fallback-placeholders). |
| `ESSIE3_FALLBACK_INLINE_EXTENSIONS` | *(see below)*     | Which fallback extensions are served `inline` vs. `attachment`. See [Content disposition](#content-disposition). |
| `ESSIE3_ACCESS_KEY`                 | *(unset)*         | When set, requires this access key on requests. See [Auth](#auth-optional). |
| `ESSIE3_FALLBACK_PUBLIC`            | `false`           | When auth is on, `true` serves fallbacks anonymously. See [Auth](#auth-optional). |
| `ESSIE3_DEBUG`                      | *(unset)*         | When `true`, logs full request/response details to stderr.                  |
| `ESSIE3_ADMIN_PORT`                 | *(unset)*         | When set, serves the read-only admin dashboard on this port. See [Admin dashboard](#admin-dashboard). |
| `ESSIE3_ADMIN_HOST`                 | `127.0.0.1`       | Interface the admin dashboard binds. Set to `0.0.0.0` to reach it through a container's published port. See [Admin dashboard](#admin-dashboard). |

## Usage

### With the AWS CLI

```sh
aws --endpoint-url http://localhost:9000 s3 mb s3://mybucket
aws --endpoint-url http://localhost:9000 s3 cp photo.jpg s3://mybucket/photos/photo.jpg
aws --endpoint-url http://localhost:9000 s3 cp s3://mybucket/photos/photo.jpg ./downloaded.jpg
```

### With curl

```sh
# Create bucket
curl -X PUT http://localhost:9000/mybucket

# Put object
curl -X PUT --data-binary @photo.jpg \
  -H "Content-Type: image/jpeg" \
  http://localhost:9000/mybucket/photos/photo.jpg

# Get object
curl http://localhost:9000/mybucket/photos/photo.jpg -o out.jpg

# Head object
curl -I http://localhost:9000/mybucket/photos/photo.jpg

# Delete object
curl -X DELETE http://localhost:9000/mybucket/photos/photo.jpg
```

### Browser upload (POST form)

```sh
curl -X POST http://localhost:9000/mybucket \
  -F "key=uploads/photo.jpg" \
  -F "file=@photo.jpg"
```

## Range requests

GET and HEAD on objects and fallback placeholders honor the
[HTTP `Range` header](https://datatracker.ietf.org/doc/html/rfc9110#section-14.2)
in its three single-range forms:

```sh
curl -H "Range: bytes=0-4"   http://localhost:9000/mybucket/photos/photo.jpg
curl -H "Range: bytes=1024-" http://localhost:9000/mybucket/photos/photo.jpg
curl -H "Range: bytes=-256"  http://localhost:9000/mybucket/photos/photo.jpg
```

Responses include `Accept-Ranges: bytes`. A satisfiable Range returns
`206 Partial Content` with `Content-Range: bytes <start>-<end>/<total>` and the
sliced body. An unsatisfiable Range returns `416 Requested Range Not
Satisfiable` with an S3-shaped XML body (`<Code>InvalidRange</Code>`) and
`Content-Range: bytes */<total>`.

`If-Range: "<etag>"` is honored against the object's ETag — if the header
matches, the Range is served; if it doesn't, the full body is served as a `200`
instead (so a client resuming an interrupted download never merges bytes from a
changed object). `If-Range` with a date value is treated as a mismatch.

> [!NOTE]
> Multi-range requests (`Range: bytes=0-100, 200-300`) are not supported;
> essie3 ignores them and serves the full body.

## Auth (optional)

essie3 is unauthenticated by default. Set `ESSIE3_ACCESS_KEY` to require a
specific access key on incoming requests — useful for integration tests that
assert "unauthenticated requests get 403."

> [!NOTE]
> Only the access key is compared; **signatures are not verified**. This is a
> test convenience, not real authentication.

```sh
ESSIE3_ACCESS_KEY=AKIATEST go run .

# Unauthenticated request → 403 AccessDenied
curl -i http://localhost:9000/mybucket/key

# Wrong key → 403 InvalidAccessKeyId
AWS_ACCESS_KEY_ID=WRONGKEY AWS_SECRET_ACCESS_KEY=anything \
aws --endpoint-url http://localhost:9000 s3 ls s3://mybucket

# Correct key → served normally
AWS_ACCESS_KEY_ID=AKIATEST AWS_SECRET_ACCESS_KEY=anything \
aws --endpoint-url http://localhost:9000 s3 ls s3://mybucket
```

Objects stored with `x-amz-acl: public-read` are readable without credentials
even when auth is enabled. Set the ACL on upload:

```sh
# AWS CLI
aws --endpoint-url http://localhost:9000 \
    s3 cp photo.jpg s3://mybucket/photos/photo.jpg --acl public-read

# curl
curl -X PUT --data-binary @photo.jpg \
  -H "Content-Type: image/jpeg" \
  -H "x-amz-acl: public-read" \
  http://localhost:9000/mybucket/photos/photo.jpg
```

> [!TIP]
> When debugging an auth failure, set `ESSIE3_DEBUG=true` to print the full
> `Authorization` header and the chosen response status to stderr.

## Fallback placeholders

Put any number of images, PDFs, or videos in the fallback directory. On
GET/HEAD for a missing object, essie3 picks one deterministically based on the
key (same key → same placeholder) and serves it with HTTP `200`.

```text
fallback-data/
├── generic1.jpg
├── generic2.jpg
├── generic.png
├── generic.pdf
└── generic.mp4
```

`ESSIE3_FALLBACK_MODE` controls how a miss is filled:

| Mode          | Behavior                                                                  |
| ------------- | ------------------------------------------------------------------------- |
| `prefer-pool` | **Default** — curated pool first; generate an identicon if no pool match. |
| `pool`        | Only curated files from the fallback dir; no generation.                  |
| `generate`    | Only generated images. PNG and JPEG only; other extensions → `NoSuchKey`. |

Under `pool`, if a key's extension has no matching placeholder, essie3 returns
the usual `NoSuchKey` error.

### Generated placeholders

When the curated pool has no match (or under `generate`), essie3 renders a
**bubble identicon** seeded by `MD5(key)`: 16 translucent overlapping circles
on a white 512×512 canvas, colored from the D3 `category20` palette. The same
key always produces byte-identical output.

<p align="center">
  <img src=".github/assets/identicon-example.png" alt="Example generated bubble identicon" width="320">
</p>

Generated responses include `ETag: "<md5 hex>"` (S3 single-PUT convention) and
a `Last-Modified` set to the server's start time, so `If-None-Match` and
`If-Range` work naturally. Set `ESSIE3_FALLBACK_MODE=pool` to disable the
generator entirely.

### Content disposition

`ESSIE3_FALLBACK_INLINE_EXTENSIONS` is a comma-separated list of extensions
served with `Content-Disposition: inline`; everything else is served as
`attachment` (so browsers prompt a download). Set it to an empty string to
serve **all** fallbacks as attachments.

```sh
ESSIE3_FALLBACK_INLINE_EXTENSIONS=.jpg,.png,.pdf go run .
```

The default inline set is:

```text
.jpg  .jpeg  .png  .gif  .webp  .pdf  .mp4  .mov  .webm  .avi
```

## Admin dashboard

Set `ESSIE3_ADMIN_PORT` to serve a small **read-only** web dashboard for
inspecting a running server:

```sh
ESSIE3_ADMIN_PORT=9001 go run .
# open http://127.0.0.1:9001
```

The dashboard (`/`) has three sections:

- **Overview** — uptime, bucket count, total objects, total size on disk,
  and the fallback hit rate. Live: uptime ticks client-side and the stats
  refresh as requests arrive.
- **Buckets** — every bucket with its object count and size; each name
  links to that bucket's own page listing its objects (key, size,
  content-type, ACL, created-at). Object keys link to the file on the S3
  server, opened in a new tab. When auth is enabled, only `public-read`
  objects are linked (others would be denied to an unauthenticated
  browser); they show as plain text instead.
- **Live traffic** — every request streamed in as it happens (method,
  bucket/key, status, and outcome: `real`, `fallback`, `miss`, `denied`,
  `write`, or `delete`), over Server-Sent Events. Writes and deletes also
  soft-refresh the listing, so new objects appear without a reload.

The dashboard is **observational only** — no upload, delete, or
configuration. It is served on a **separate port** so it never collides
with the S3 API, and binds `127.0.0.1` by default since it has no
authentication of its own. Leave `ESSIE3_ADMIN_PORT` unset to disable it
entirely (the default).

> [!IMPORTANT]
> **In Docker**, a process bound to `127.0.0.1` inside the container is
> unreachable through a published port — Docker forwards to the
> container's external interface, not its loopback. Set
> `ESSIE3_ADMIN_HOST=0.0.0.0` so the dashboard binds all interfaces
> inside the container; the container boundary and your `ports:` mapping
> then control exposure. For example:
>
> ```yaml
> environment:
>   ESSIE3_ADMIN_PORT: "9001"
>   ESSIE3_ADMIN_HOST: "0.0.0.0"
> ports:
>   - "9001:9001"
> ```

## Storage layout

```text
data/
└── <bucket>/
    ├── <key>              # raw body
    └── <key>.meta.json    # content-type, etag, created-at, acl, ...
```

Metadata is written atomically alongside the body. PUT and DELETE on the same
key are serialized via an in-process per-key lock so concurrent writers cannot
leave a body/meta mismatch.

> [!NOTE]
> essie3 does not coordinate across multiple processes sharing the same
> `DATA_DIR`.

## Development

```sh
go test ./...     # full test suite
go vet ./...
gofmt -l .        # lists files needing formatting
```

## License

MIT — see [LICENSE](LICENSE).

The admin dashboard bundles [Pico.css](https://picocss.com) (classless
build, v2.0.6), vendored as `pico.classless.min.css` and also licensed
under MIT.
