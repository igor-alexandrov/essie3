package main

import (
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var placeholderExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true,
	".pdf": true,
	".mp4": true, ".mov": true, ".webm": true, ".avi": true,
}

// extAliases maps extensions to a canonical form so that
// different extensions for the same format share one pool.
var extAliases = map[string]string{
	".jpeg": ".jpg",
}

type Placeholder struct {
	Path        string
	Body        []byte
	ContentType string
}

type Fallback struct {
	all   []*Placeholder
	byExt map[string][]*Placeholder
}

func NewFallback(dir string) (*Fallback, error) {
	fb := &Fallback{byExt: make(map[string][]*Placeholder)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fb, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !placeholderExtensions[ext] {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			log.Printf("fallback: skip %s: %v", path, err)
			continue
		}
		p := &Placeholder{
			Path:        path,
			Body:        body,
			ContentType: http.DetectContentType(body),
		}
		canonical := normalizeExt(ext)
		fb.all = append(fb.all, p)
		fb.byExt[canonical] = append(fb.byExt[canonical], p)
	}

	return fb, nil
}

func (fb *Fallback) Count() int {
	return len(fb.all)
}

func normalizeExt(ext string) string {
	if alias, ok := extAliases[ext]; ok {
		return alias
	}
	return ext
}

func (fb *Fallback) Select(key string) *Placeholder {
	ext := normalizeExt(strings.ToLower(filepath.Ext(key)))
	pool := fb.byExt[ext]
	if len(pool) == 0 {
		return nil
	}

	h := fnv.New32a()
	h.Write([]byte(key))
	return pool[int(h.Sum32())%len(pool)]
}
