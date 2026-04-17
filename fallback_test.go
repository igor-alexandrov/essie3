package main

import (
	"strings"
	"testing"
)

func TestFallbackLoad(t *testing.T) {
	fb, err := NewFallback("testdata/fallback")
	if err != nil {
		t.Fatalf("NewFallback: %v", err)
	}
	if len(fb.all) != 3 {
		t.Fatalf("loaded %d placeholders, want 3", len(fb.all))
	}
	if len(fb.byExt[".jpg"]) != 2 {
		t.Fatalf("jpg count = %d, want 2", len(fb.byExt[".jpg"]))
	}
	if len(fb.byExt[".pdf"]) != 1 {
		t.Fatalf("pdf count = %d, want 1", len(fb.byExt[".pdf"]))
	}
}

func TestFallbackLoad_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	fb, err := NewFallback(dir)
	if err != nil {
		t.Fatalf("NewFallback: %v", err)
	}
	if len(fb.all) != 0 {
		t.Fatalf("loaded %d images, want 0", len(fb.all))
	}
}

func TestFallbackSelect_Deterministic(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback")

	img1 := fb.Select("some/key.jpg")
	img2 := fb.Select("some/key.jpg")

	if img1.Path != img2.Path {
		t.Fatalf("same key returned different images: %q vs %q", img1.Path, img2.Path)
	}
}

func TestFallbackSelect_DifferentKeys(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback")

	img1 := fb.Select("key-a.jpg")
	img2 := fb.Select("key-b.jpg")
	if img1.Body == nil || img2.Body == nil {
		t.Fatal("expected non-nil bodies")
	}
}

func TestFallbackSelect_MatchesExtension(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback")

	// PDF key should get the PDF placeholder
	p := fb.Select("document/report.pdf")
	if p == nil {
		t.Fatal("expected placeholder")
	}
	if !strings.HasSuffix(p.Path, ".pdf") {
		t.Fatalf("expected PDF placeholder, got %q", p.Path)
	}

	// JPG key should get a JPG placeholder
	p = fb.Select("images/photo.jpg")
	if p == nil {
		t.Fatal("expected placeholder")
	}
	if !strings.HasSuffix(p.Path, ".jpg") {
		t.Fatalf("expected JPG placeholder, got %q", p.Path)
	}
}

func TestFallbackSelect_NilForUnmatchedExtension(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback")

	p := fb.Select("data/export.csv")
	if p != nil {
		t.Fatal("expected nil for extension with no placeholders")
	}
}

func TestFallbackSelect_NoImages(t *testing.T) {
	dir := t.TempDir()
	fb, _ := NewFallback(dir)

	img := fb.Select("any.jpg")
	if img != nil {
		t.Fatal("expected nil when no fallback images")
	}
}
