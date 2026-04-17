package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"
)

type S3Error struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
	Key     string   `xml:"Key,omitempty"`
	Bucket  string   `xml:"BucketName,omitempty"`
}

type CreateBucketResult struct {
	XMLName xml.Name `xml:"CreateBucketConfiguration"`
}

type CopyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

func writeXMLError(w http.ResponseWriter, status int, code, message, bucket, key string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	resp := S3Error{Code: code, Message: message, Key: key, Bucket: bucket}
	out, _ := xml.MarshalIndent(resp, "", "  ") // cannot fail for this struct
	fmt.Fprintf(w, "%s%s", xml.Header, out)
}

func writeNoSuchKey(w http.ResponseWriter, bucket, key string) {
	writeXMLError(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.", bucket, key)
}

func writeNoSuchBucket(w http.ResponseWriter, bucket string) {
	writeXMLError(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.", bucket, "")
}

func writeCopyObjectResult(w http.ResponseWriter, etag string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	resp := CopyObjectResult{ETag: etag, LastModified: time.Now().UTC().Format(time.RFC3339)}
	out, _ := xml.MarshalIndent(resp, "", "  ") // cannot fail for this struct
	fmt.Fprintf(w, "%s%s", xml.Header, out)
}
