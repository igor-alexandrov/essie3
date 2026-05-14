package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	storage  *Storage
	fallback *Fallback
	auth     AuthConfig
}

func NewHandler(storage *Storage, fallback *Fallback, auth AuthConfig) http.Handler {
	return &Handler{storage: storage, fallback: fallback, auth: auth}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setCORS(w)

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
		if e := h.auth.authorize(r, opWrite, ""); e != nil {
			writeAuthError(w, e, bucket, "")
			return
		}
		if err := h.storage.CreateBucket(bucket); err != nil {
			writeXMLError(w, http.StatusBadRequest, "InvalidBucketName", err.Error(), bucket, "")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><CreateBucketConfiguration/>`)
	case http.MethodHead:
		if e := h.auth.authorize(r, opWrite, ""); e != nil {
			writeAuthError(w, e, bucket, "")
			return
		}
		if h.storage.BucketExists(bucket) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodGet:
		if e := h.auth.authorize(r, opWrite, ""); e != nil {
			writeAuthError(w, e, bucket, "")
			return
		}
		if !h.storage.BucketExists(bucket) {
			writeNoSuchBucket(w, bucket)
			return
		}
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
		if e := h.auth.authorize(r, opWrite, ""); e != nil {
			writeAuthError(w, e, bucket, key)
			return
		}
		if copySource := r.Header.Get("x-amz-copy-source"); copySource != "" {
			h.handleCopyObject(w, bucket, key, copySource)
		} else {
			h.handlePutObject(w, r, bucket, key)
		}
	case http.MethodGet:
		h.handleGetObject(w, r, bucket, key)
	case http.MethodHead:
		h.handleHeadObject(w, r, bucket, key)
	case http.MethodDelete:
		if e := h.auth.authorize(r, opWrite, ""); e != nil {
			writeAuthError(w, e, bucket, key)
			return
		}
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

func (h *Handler) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	obj, objErr := h.storage.GetObject(bucket, key)

	var acl string
	if objErr == nil {
		acl = obj.Meta.ACL
	}

	authErr := h.auth.authorize(r, opRead, acl)

	if objErr != nil {
		if p := h.fallback.Select(key); p != nil {
			if authErr != nil && !h.auth.FallbackPublic {
				writeAuthError(w, authErr, bucket, key)
				return
			}
			totalLen := int64(len(p.Body))
			out := evaluateRange(r, totalLen, "")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
			switch {
			case out.serveFull:
				w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
				w.WriteHeader(http.StatusOK)
				w.Write(p.Body)
			case out.bounds != nil:
				s, e := out.bounds.start, out.bounds.end
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
				w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(p.Body[s : e+1])
			default:
				writeInvalidRange(w, bucket, key, totalLen)
			}
			return
		}
		if authErr != nil {
			writeAuthError(w, authErr, bucket, key)
			return
		}
		writeNoSuchKey(w, bucket, key)
		return
	}

	if authErr != nil {
		writeAuthError(w, authErr, bucket, key)
		return
	}

	totalLen := int64(len(obj.Body))
	out := evaluateRange(r, totalLen, obj.Meta.ETag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", obj.Meta.ContentType)
	w.Header().Set("ETag", obj.Meta.ETag)
	w.Header().Set("Last-Modified", obj.Meta.CreatedAt.UTC().Format(http.TimeFormat))
	if obj.Meta.ContentDisposition != "" {
		w.Header().Set("Content-Disposition", obj.Meta.ContentDisposition)
	}
	switch {
	case out.serveFull:
		w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
		w.WriteHeader(http.StatusOK)
		w.Write(obj.Body)
	case out.bounds != nil:
		s, e := out.bounds.start, out.bounds.end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
		w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(obj.Body[s : e+1])
	default:
		writeInvalidRange(w, bucket, key, totalLen)
	}
}

func (h *Handler) handleHeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, metaErr := h.storage.HeadObject(bucket, key)

	var acl string
	if metaErr == nil {
		acl = meta.ACL
	}

	authErr := h.auth.authorize(r, opRead, acl)

	if metaErr != nil {
		if p := h.fallback.Select(key); p != nil {
			if authErr != nil && !h.auth.FallbackPublic {
				writeAuthError(w, authErr, bucket, key)
				return
			}
			totalLen := int64(len(p.Body))
			out := evaluateRange(r, totalLen, "")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Content-Disposition", h.fallback.Disposition(key))
			switch {
			case out.serveFull:
				w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
				w.WriteHeader(http.StatusOK)
			case out.bounds != nil:
				s, e := out.bounds.start, out.bounds.end
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
				w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
				w.WriteHeader(http.StatusPartialContent)
			default:
				writeInvalidRange(w, bucket, key, totalLen)
			}
			return
		}
		if authErr != nil {
			writeAuthError(w, authErr, bucket, key)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if authErr != nil {
		writeAuthError(w, authErr, bucket, key)
		return
	}

	totalLen := meta.ContentLength
	out := evaluateRange(r, totalLen, meta.ETag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.CreatedAt.UTC().Format(http.TimeFormat))
	switch {
	case out.serveFull:
		w.Header().Set("Content-Length", strconv.FormatInt(totalLen, 10))
		w.WriteHeader(http.StatusOK)
	case out.bounds != nil:
		s, e := out.bounds.start, out.bounds.end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, totalLen))
		w.Header().Set("Content-Length", strconv.FormatInt(e-s+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	default:
		writeInvalidRange(w, bucket, key, totalLen)
	}
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
	if e := h.auth.authorize(r, opWrite, ""); e != nil {
		writeAuthError(w, e, bucket, "")
		return
	}
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
