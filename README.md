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
- Deterministic fallback placeholders by file extension
  (.jpg / .jpeg / .png / .gif / .webp / .pdf / .mp4 / .mov / .webm / .avi)
- Atomic object writes (temp-file + rename)
- Path-traversal protection on bucket and key names
- Graceful shutdown on `SIGINT` / `SIGTERM`

This is **not** a production S3 replacement — no auth, no signing, no
versioning, no real `ListObjects`.

## Running

### From source

```sh
go run .
```

### With Docker

```sh
docker build -t essie3 .
docker run --rm -p 9000:9000 \
  -v $PWD/data:/data \
  -v $PWD/fallback-images:/fallback \
  -e DATA_DIR=/data \
  -e FALLBACK_DATA_DIR=/fallback \
  essie3
```

### Configuration

All configuration via environment variables:

| Variable                     | Default           | Description                       |
| ---------------------------- | ----------------- | --------------------------------- |
| `PORT`                       | `9000`            | HTTP port to listen on            |
| `DATA_DIR`                   | `./data`          | Where uploaded objects are stored |
| `FALLBACK_DATA_DIR`          | `./fallback-data` | Directory of fallback placeholders|
| `FALLBACK_INLINE_EXTENSIONS` | `.jpg`, `.jpeg`<br>`.png`, `.gif`, `.webp`<br>`.pdf`<br>`.mp4`, `.mov`, `.webm`, `.avi` | Comma-separated extensions served inline on fallback responses; everything else is served as `attachment`. Set to empty string to serve all fallbacks as attachments. Example: `FALLBACK_INLINE_EXTENSIONS=.jpg,.png,.pdf` |

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
