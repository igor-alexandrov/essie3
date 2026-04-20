package main

import (
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultInlineExtensions is the set of extensions served with
// Content-Disposition: inline when a fallback placeholder is returned.
// Extensions outside this set (e.g. future docx/xlsx placeholders) are
// served as attachments so browsers prompt a download.
var DefaultInlineExtensions = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp",
	".pdf",
	".mp4", ".mov", ".webm", ".avi",
}

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

// normalizeExtInput accepts ".jpg", "jpg", " JPG " and returns ".jpg".
// Empty/whitespace input returns "".
func normalizeExtInput(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// ParseExtList splits a comma-separated list of extensions and normalizes
// each one. Returns a non-nil slice so callers can distinguish "unset"
// (nil) from "explicitly empty".
func ParseExtList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if e := normalizeExtInput(part); e != "" {
			out = append(out, e)
		}
	}
	return out
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
