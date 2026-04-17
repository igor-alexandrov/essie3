package main

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ObjectMeta struct {
	ContentType        string    `json:"content_type"`
	ContentLength      int64     `json:"content_length"`
	ETag               string    `json:"etag"`
	ACL                string    `json:"acl,omitempty"`
	ContentDisposition string    `json:"content_disposition,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type StoredObject struct {
	Body []byte
	Meta *ObjectMeta
}

type Storage struct {
	dataDir string
}

func NewStorage(dataDir string) *Storage {
	return &Storage{dataDir: dataDir}
}

var errInvalidName = errors.New("invalid bucket or key name")

// validateName rejects empty, absolute, or traversing paths. Called for
// bucket and each key on every request path so the filesystem layer can
// trust its inputs.
func validateName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") {
		return errInvalidName
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return errInvalidName
		}
	}
	return nil
}

func (s *Storage) objectPath(bucket, key string) string {
	return filepath.Join(s.dataDir, bucket, key)
}

func (s *Storage) metaPath(bucket, key string) string {
	return s.objectPath(bucket, key) + ".meta.json"
}

func (s *Storage) PutObject(bucket, key string, body []byte, meta *ObjectMeta) (string, error) {
	if err := validateName(bucket); err != nil {
		return "", err
	}
	if err := validateName(key); err != nil {
		return "", err
	}

	objPath := s.objectPath(bucket, key)

	if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	etag := fmt.Sprintf("\"%x\"", md5.Sum(body))
	meta.ETag = etag
	meta.ContentLength = int64(len(body))
	meta.CreatedAt = time.Now().UTC()

	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal meta: %w", err)
	}

	if err := writeFileAtomic(objPath, body, 0o644); err != nil {
		return "", fmt.Errorf("write object: %w", err)
	}
	if err := writeFileAtomic(s.metaPath(bucket, key), metaBytes, 0o644); err != nil {
		return "", fmt.Errorf("write meta: %w", err)
	}

	return etag, nil
}

func (s *Storage) GetObject(bucket, key string) (*StoredObject, error) {
	if err := validateName(bucket); err != nil {
		return nil, err
	}
	if err := validateName(key); err != nil {
		return nil, err
	}

	body, err := os.ReadFile(s.objectPath(bucket, key))
	if err != nil {
		return nil, err
	}

	meta, err := s.readMeta(bucket, key)
	if err != nil {
		return nil, err
	}

	return &StoredObject{Body: body, Meta: meta}, nil
}

func (s *Storage) HeadObject(bucket, key string) (*ObjectMeta, error) {
	if err := validateName(bucket); err != nil {
		return nil, err
	}
	if err := validateName(key); err != nil {
		return nil, err
	}
	return s.readMeta(bucket, key)
}

func (s *Storage) DeleteObject(bucket, key string) {
	if err := validateName(bucket); err != nil {
		return
	}
	if err := validateName(key); err != nil {
		return
	}
	if err := os.Remove(s.objectPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete object %s/%s: %v", bucket, key, err)
	}
	if err := os.Remove(s.metaPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete meta %s/%s: %v", bucket, key, err)
	}
}

func (s *Storage) CopyObject(srcBucket, srcKey, dstBucket, dstKey string) (string, error) {
	obj, err := s.GetObject(srcBucket, srcKey)
	if err != nil {
		return "", err
	}
	metaCopy := *obj.Meta
	return s.PutObject(dstBucket, dstKey, obj.Body, &metaCopy)
}

func (s *Storage) BucketExists(bucket string) bool {
	if err := validateName(bucket); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(s.dataDir, bucket))
	return err == nil && info.IsDir()
}

func (s *Storage) CreateBucket(bucket string) error {
	if err := validateName(bucket); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.dataDir, bucket), 0o755)
}

func (s *Storage) readMeta(bucket, key string) (*ObjectMeta, error) {
	metaBytes, err := os.ReadFile(s.metaPath(bucket, key))
	if err != nil {
		return nil, err
	}
	var meta ObjectMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	return &meta, nil
}

// writeFileAtomic writes data to a sibling temp file and renames it into
// place so a crashed or concurrent writer can never leave a torn file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
