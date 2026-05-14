# essie3

A tiny, filesystem-backed S3-compatible server for local development and
testing. Speaks enough of the S3 REST API to stand in for AWS S3 when
running integration tests, demos, or offline dev environments.

When an object is missing, essie3 can return a deterministic **fallback
placeholder** (e.g. a generic image or PDF) instead of `404 NoSuchKey` —
useful when seeding a dev environment where the real assets don't exist
yet, so your UI doesn't render broken images.

## Features

- S3-style `PUT`, `GET`, `HEAD`, `DELETE`, `POST` (multipart), and `COPY`
  for objects
- Bucket create / head / list (stub)
- CORS enabled for browser uploads
- Per-object metadata persisted as JSON sidecar files
- HTTP `Range` requests (single-range) with `If-Range` ETag matching
  on objects and fallback placeholders
- Deterministic fallback placeholders by file extension
  (.jpg / .jpeg / .png / .gif / .webp / .pdf / .mp4 / .mov / .webm / .avi)
- Atomic object writes (temp-file + rename)
- Path-traversal protection on bucket and key names
- Optional access-key auth with `x-amz-acl: public-read` escape hatch
- Graceful shutdown on `SIGINT` / `SIGTERM`

This is **not** a production S3 replacement — no SigV4 signature
verification, no versioning, no real `ListObjects`. (Optional
access-key auth is available for tests that need to exercise the
auth-failure path; see the `ESSIE3_ACCESS_KEY` env var below.)

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
      DATA_DIR: /data
      FALLBACK_DATA_DIR: /fallback-data
```

Then run:

```sh
docker compose up
```

### Configuration

All configuration via environment variables:

| Variable                     | Default           | Description                       |
| ---------------------------- | ----------------- | --------------------------------- |
| `PORT`                       | `9000`            | HTTP port to listen on            |
| `DATA_DIR`                   | `./data`          | Where uploaded objects are stored |
| `FALLBACK_DATA_DIR`          | `./fallback-data` | Directory of fallback placeholders|
| `FALLBACK_INLINE_EXTENSIONS` | `.jpg`, `.jpeg`<br>`.png`, `.gif`, `.webp`<br>`.pdf`<br>`.mp4`, `.mov`, `.webm`, `.avi` | Comma-separated extensions served inline on fallback responses; everything else is served as `attachment`. Set to empty string to serve all fallbacks as attachments. Example: `FALLBACK_INLINE_EXTENSIONS=.jpg,.png,.pdf` |
| `ESSIE3_ACCESS_KEY`          | *(unset)*         | When set, essie3 requires requests to present this key in the `Authorization` header's SigV4 `Credential=` portion. Signatures are not verified — only the access-key string is compared. When unset, all requests are served anonymously (default behavior). |
| `ESSIE3_FALLBACK_PUBLIC`     | `false`           | Only relevant when `ESSIE3_ACCESS_KEY` is set. `true` → fallback placeholders are served anonymously even without credentials. `false` → fallbacks follow the same auth check as real objects. |
| `ESSIE3_DEBUG`               | *(unset)*         | When set to `true`, log full request and response details (method, path, headers, status, timing) to stderr. Useful when debugging integration tests, especially auth-failure paths. Off by default. |

## Usage

### With the AWS CLI

```sh
aws --endpoint-url http://localhost:9000 \
    s3 mb s3://mybucket
aws --endpoint-url http://localhost:9000 \
    s3 cp photo.jpg s3://mybucket/photos/photo.jpg
aws --endpoint-url http://localhost:9000 \
    s3 cp s3://mybucket/photos/photo.jpg ./downloaded.jpg
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
`206 Partial Content` with `Content-Range: bytes <start>-<end>/<total>`
and the sliced body. An unsatisfiable Range returns
`416 Requested Range Not Satisfiable` with an S3-shaped XML body
(`<Code>InvalidRange</Code>`) and `Content-Range: bytes */<total>`.

`If-Range: "<etag>"` is honored against the object's ETag — if the
header matches, the Range is served; if it doesn't, the full body is
served as a 200 instead (so a client resuming an interrupted download
never merges bytes from a changed object). `If-Range` with a date
value is treated as a mismatch.

Multi-range requests (`Range: bytes=0-100, 200-300`) are not
supported; essie3 ignores them and serves the full body.

## Auth (optional)

essie3 is unauthenticated by default. Set `ESSIE3_ACCESS_KEY` to require
a specific access key on incoming requests — useful for integration
tests that assert "unauthenticated requests get 403." Only the access
key is compared; signatures are not verified.

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

Objects stored with `x-amz-acl: public-read` are readable without
credentials even when auth is enabled. Set the ACL on upload:

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

When debugging an auth failure, set `ESSIE3_DEBUG=true` to print the
full `Authorization` header and the chosen response status to stderr.

## Fallback placeholders

Put any number of images, PDFs, or videos in the fallback directory. On
GET/HEAD for a missing object, essie3 picks one deterministically based
on the key (same key → same placeholder) and serves it with HTTP 200.

```
fallback-data/
├── generic1.jpg
├── generic2.jpg
├── generic.png
├── generic.pdf
└── generic.mp4
```

If a key's extension has no matching placeholders, essie3 returns the
usual `NoSuchKey` error.

## Storage layout

```
data/
└── <bucket>/
    └── <key>              # raw body
    └── <key>.meta.json    # content-type, etag, created-at, acl, ...
```

Metadata is written atomically alongside the body.

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
```

## License

MIT — see [LICENSE](LICENSE).
