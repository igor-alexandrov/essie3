package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type Handler struct {
	storage  *Storage
	fallback *Fallback
}

func NewHandler(storage *Storage, fallback *Fallback) http.Handler {
	return &Handler{storage: storage, fallback: fallback}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setCORS(w)

	log.Printf("%s %s", r.Method, r.URL.Path)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	bucket := parts[0]

	if bucket == "" {
		writeXMLError(w, http.StatusBadRequest, "InvalidRequest", "Missing bucket", "", "")
		return
	}

	if len(parts) == 1 || parts[1] == "" {
		h.handleBucket(w, r, bucket)
		return
	}

	h.handleObject(w, r, bucket, parts[1])
}

func (h *Handler) setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "ETag, Location, x-amz-request-id")
}

func (h *Handler) handleBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodPut:
		if err := h.storage.CreateBucket(bucket); err != nil {
			writeXMLError(w, http.StatusBadRequest, "InvalidBucketName", err.Error(), bucket, "")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><CreateBucketConfiguration/>`)
	case http.MethodHead:
		if h.storage.BucketExists(bucket) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodGet:
		if !h.storage.BucketExists(bucket) {
			writeNoSuchBucket(w, bucket)
			return
		}
		// ListObjects stub — return empty list.
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>%s</Name></ListBucketResult>`, bucket)
	case http.MethodPost:
		h.handlePostObject(w, r, bucket)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	switch r.Method {
	case http.MethodPut:
		if copySource := r.Header.Get("x-amz-copy-source"); copySource != "" {
			h.handleCopyObject(w, bucket, key, copySource)
		} else {
			h.handlePutObject(w, r, bucket, key)
		}
	case http.MethodGet:
		h.handleGetObject(w, bucket, key)
	case http.MethodHead:
		h.handleHeadObject(w, bucket, key)
	case http.MethodDelete:
		h.storage.DeleteObject(bucket, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket, key)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}

	meta := &ObjectMeta{
		ContentType:        contentType,
		ACL:                r.Header.Get("x-amz-acl"),
		ContentDisposition: r.Header.Get("Content-Disposition"),
	}

	etag, err := h.storage.PutObject(bucket, key, body, meta)
	if err != nil {
		if errors.Is(err, errInvalidName) {
			writeXMLError(w, http.StatusBadRequest, "InvalidArgument", err.Error(), bucket, key)
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket, key)
		return
	}

	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleGetObject(w http.ResponseWriter, bucket, key string) {
	obj, err := h.storage.GetObject(bucket, key)
	if err != nil {
		if p := h.fallback.Select(key); p != nil {
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(p.Body)))
			w.WriteHeader(http.StatusOK)
			w.Write(p.Body)
			return
		}
		writeNoSuchKey(w, bucket, key)
		return
	}

	w.Header().Set("Content-Type", obj.Meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Meta.ContentLength))
	w.Header().Set("ETag", obj.Meta.ETag)
	w.Header().Set("Last-Modified", obj.Meta.CreatedAt.UTC().Format(http.TimeFormat))
	if obj.Meta.ContentDisposition != "" {
		w.Header().Set("Content-Disposition", obj.Meta.ContentDisposition)
	}
	w.WriteHeader(http.StatusOK)
	w.Write(obj.Body)
}

func (h *Handler) handleHeadObject(w http.ResponseWriter, bucket, key string) {
	meta, err := h.storage.HeadObject(bucket, key)
	if err != nil {
		if p := h.fallback.Select(key); p != nil {
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(p.Body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.ContentLength))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.CreatedAt.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCopyObject(w http.ResponseWriter, dstBucket, dstKey, copySource string) {
	// copySource format: /<bucket>/<key>
	source := strings.TrimPrefix(copySource, "/")
	parts := strings.SplitN(source, "/", 2)
	if len(parts) != 2 {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", "Invalid x-amz-copy-source", dstBucket, dstKey)
		return
	}
	srcBucket, srcKey := parts[0], parts[1]

	etag, err := h.storage.CopyObject(srcBucket, srcKey, dstBucket, dstKey)
	if err != nil {
		writeNoSuchKey(w, srcBucket, srcKey)
		return
	}

	writeCopyObjectResult(w, etag)
}

func (h *Handler) handlePostObject(w http.ResponseWriter, r *http.Request, bucket string) {
	if err := r.ParseMultipartForm(50 << 20); err != nil { // 50MB max
		writeXMLError(w, http.StatusBadRequest, "MalformedPOSTRequest", err.Error(), bucket, "")
		return
	}

	key := r.FormValue("key")
	if key == "" {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", "Missing key field", bucket, "")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeXMLError(w, http.StatusBadRequest, "InvalidArgument", "Missing file field", bucket, "")
		return
	}
	defer file.Close()

	body, err := io.ReadAll(file)
	if err != nil {
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket, key)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}

	meta := &ObjectMeta{
		ContentType: contentType,
		ACL:         r.FormValue("acl"),
	}

	etag, err := h.storage.PutObject(bucket, key, body, meta)
	if err != nil {
		if errors.Is(err, errInvalidName) {
			writeXMLError(w, http.StatusBadRequest, "InvalidArgument", err.Error(), bucket, key)
			return
		}
		writeXMLError(w, http.StatusInternalServerError, "InternalError", err.Error(), bucket, key)
		return
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Location", fmt.Sprintf("/%s/%s", bucket, key))
	w.WriteHeader(http.StatusNoContent)
}
