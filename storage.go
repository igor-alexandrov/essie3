package main

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	keyMu   sync.Map // map[string]*sync.RWMutex (key = bucket+"/"+key)
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

// keyMutex returns the per-key RWMutex, lazily creating it on first
// use. Used by PutObject/DeleteObject (write lock) and GetObject/
// HeadObject (read lock) to serialize writers vs writers and readers
// vs writers, so a reader cannot observe the brief window between a
// writer's body rename and its meta rename.
func (s *Storage) keyMutex(bucket, key string) *sync.RWMutex {
	k := bucket + "/" + key
	if mu, ok := s.keyMu.Load(k); ok {
		return mu.(*sync.RWMutex)
	}
	mu, _ := s.keyMu.LoadOrStore(k, &sync.RWMutex{})
	return mu.(*sync.RWMutex)
}

func (s *Storage) PutObject(bucket, key string, body []byte, meta *ObjectMeta) (string, error) {
	if err := validateName(bucket); err != nil {
		return "", err
	}
	if err := validateName(key); err != nil {
		return "", err
	}

	mu := s.keyMutex(bucket, key)
	mu.Lock()
	defer mu.Unlock()

	objPath := s.objectPath(bucket, key)

	if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	prevBody, prevErr := os.ReadFile(objPath)
	if prevErr != nil && !os.IsNotExist(prevErr) {
		return "", fmt.Errorf("read prev body: %w", prevErr)
	}
	hadPrev := prevErr == nil

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
		if rbErr := rollbackBody(objPath, prevBody, hadPrev); rbErr != nil {
			log.Printf("rollback after meta-write failure for %s/%s: %v", bucket, key, rbErr)
		}
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

	mu := s.keyMutex(bucket, key)
	mu.RLock()
	defer mu.RUnlock()

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
	mu := s.keyMutex(bucket, key)
	mu.RLock()
	defer mu.RUnlock()
	return s.readMeta(bucket, key)
}

func (s *Storage) DeleteObject(bucket, key string) error {
	if err := validateName(bucket); err != nil {
		return err
	}
	if err := validateName(key); err != nil {
		return err
	}
	mu := s.keyMutex(bucket, key)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(s.objectPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete object %s/%s: %v", bucket, key, err)
	}
	if err := os.Remove(s.metaPath(bucket, key)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete meta %s/%s: %v", bucket, key, err)
	}
	return nil
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

// BucketInfo is one bucket's summary (the admin dashboard's <details>
// summary row).
type BucketInfo struct {
	Name        string
	ObjectCount int
	TotalBytes  int64
}

// ObjectInfo is one object row inside a bucket's <details> table.
type ObjectInfo struct {
	Key         string // forward-slash key, sidecars excluded
	Size        int64  // meta.ContentLength, else on-disk body size
	ContentType string
	ACL         string
	CreatedAt   time.Time // zero if meta missing/unparseable
}

// ListBuckets returns every immediate subdirectory of dataDir with its
// object count and total size, sorted by name. A missing dataDir yields
// an empty slice and nil error. This is a read-only, best-effort walk
// for the admin dashboard: it takes no per-key locks, so it never blocks
// or fails the S3 write path.
func (s *Storage) ListBuckets() ([]BucketInfo, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BucketInfo{}, nil
		}
		return nil, err
	}

	buckets := make([]BucketInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		objs, err := s.ListObjects(entry.Name())
		if err != nil {
			// Skip anything that isn't a listable bucket (e.g. an
			// invalid name); the dashboard is best-effort.
			continue
		}
		var total int64
		for _, o := range objs {
			total += o.Size
		}
		buckets = append(buckets, BucketInfo{
			Name:        entry.Name(),
			ObjectCount: len(objs),
			TotalBytes:  total,
		})
	}

	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	return buckets, nil
}

// ListObjects walks data/<bucket> recursively, pairing each object with
// its .meta.json sidecar (which is itself never listed). Keys are
// reconstructed with "/" separators relative to the bucket root and
// sorted. An unknown bucket returns os.ErrNotExist; an invalid name
// returns errInvalidName. Like ListBuckets it takes no locks and
// tolerates a missing/unparseable sidecar (metadata fields left zero,
// size falling back to the on-disk body length).
func (s *Storage) ListObjects(bucket string) ([]ObjectInfo, error) {
	if err := validateName(bucket); err != nil {
		return nil, err
	}

	root := filepath.Join(s.dataDir, bucket)
	if info, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	} else if !info.IsDir() {
		return nil, os.ErrNotExist
	}

	var objs []ObjectInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), ".meta.json") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)

		o := ObjectInfo{Key: key}
		if meta, err := s.readMeta(bucket, key); err == nil {
			o.Size = meta.ContentLength
			o.ContentType = meta.ContentType
			o.ACL = meta.ACL
			o.CreatedAt = meta.CreatedAt
		} else if info, err := d.Info(); err == nil {
			o.Size = info.Size()
		}
		objs = append(objs, o)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	return objs, nil
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

// rollbackBody restores objPath to its prior state after a meta-write
// failure. If hadPrev=true, prevBody is rewritten atomically; if
// hadPrev=false (the body was newly created by this PUT), objPath is
// removed. A non-existent file with hadPrev=false is treated as
// already-rolled-back (returns nil).
func rollbackBody(objPath string, prevBody []byte, hadPrev bool) error {
	if hadPrev {
		return writeFileAtomic(objPath, prevBody, 0o644)
	}
	if err := os.Remove(objPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
