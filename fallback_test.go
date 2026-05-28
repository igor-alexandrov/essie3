package main

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"
)

func TestFallbackLoad(t *testing.T) {
	fb, err := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
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
	fb, err := NewFallback(dir, DefaultInlineExtensions, FallbackModePool)
	if err != nil {
		t.Fatalf("NewFallback: %v", err)
	}
	if len(fb.all) != 0 {
		t.Fatalf("loaded %d images, want 0", len(fb.all))
	}
}

func TestFallbackSelect_Deterministic(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)

	img1 := fb.Select("some/key.jpg")
	img2 := fb.Select("some/key.jpg")

	if img1.Path != img2.Path {
		t.Fatalf("same key returned different images: %q vs %q", img1.Path, img2.Path)
	}
}

func TestFallbackSelect_DifferentKeys(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)

	img1 := fb.Select("key-a.jpg")
	img2 := fb.Select("key-b.jpg")
	if img1.Body == nil || img2.Body == nil {
		t.Fatal("expected non-nil bodies")
	}
}

func TestFallbackSelect_MatchesExtension(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)

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
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)

	p := fb.Select("data/export.csv")
	if p != nil {
		t.Fatal("expected nil for extension with no placeholders")
	}
}

func TestFallbackSelect_NoImages(t *testing.T) {
	dir := t.TempDir()
	fb, _ := NewFallback(dir, DefaultInlineExtensions, FallbackModePool)

	img := fb.Select("any.jpg")
	if img != nil {
		t.Fatal("expected nil when no fallback images")
	}
}

func TestParseExtList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"jpg", []string{".jpg"}},
		{".jpg", []string{".jpg"}},
		{"JPG", []string{".jpg"}},
		{"jpeg", []string{".jpeg"}},
		{" .jpg , png ,  WEBP ", []string{".jpg", ".png", ".webp"}},
		{",,", []string{}},
	}
	for _, c := range cases {
		got := ParseExtList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("ParseExtList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseExtList(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestDefaultInlineExtensions_CoversCurrentFallbackSet(t *testing.T) {
	want := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf", ".mp4", ".mov", ".webm", ".avi"}
	set := map[string]bool{}
	for _, e := range DefaultInlineExtensions {
		set[e] = true
	}
	for _, e := range want {
		if !set[e] {
			t.Errorf("DefaultInlineExtensions missing %q", e)
		}
	}
}

func TestFallbackDisposition_InlineForDefaults(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
	got := fb.Disposition("photos/sunset.jpg")
	want := `inline; filename="sunset.jpg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_AttachmentForUnlisted(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)
	got := fb.Disposition("docs/report.docx")
	want := `attachment; filename="report.docx"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_CustomList(t *testing.T) {
	// Explicit empty list → everything is attachment.
	fb, _ := NewFallback("testdata/fallback", []string{}, FallbackModePool)
	got := fb.Disposition("images/a.jpg")
	want := `attachment; filename="a.jpg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}

	// Custom list adds docx as inline.
	fb2, _ := NewFallback("testdata/fallback", []string{".docx"}, FallbackModePool)
	got = fb2.Disposition("reports/q1.docx")
	want = `inline; filename="q1.docx"`
	if got != want {
		t.Fatalf("Disposition custom = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_JpegAliasedToJpg(t *testing.T) {
	// Inline list contains .jpg; requesting .jpeg should still be inline.
	fb, _ := NewFallback("testdata/fallback", []string{".jpg"}, FallbackModePool)
	got := fb.Disposition("pic.jpeg")
	want := `inline; filename="pic.jpeg"`
	if got != want {
		t.Fatalf("Disposition = %q, want %q", got, want)
	}
}

func TestFallbackDisposition_SanitizesNonASCII(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModePool)

	cases := []struct {
		key  string
		want string
	}{
		{"photos/café.jpg", `inline; filename="caf__.jpg"`},
		{"weird/\"name\".jpg", `inline; filename="_name_.jpg"`},
		{"ctl/\x01name.jpg", `inline; filename="_name.jpg"`},
	}
	for _, c := range cases {
		got := fb.Disposition(c.key)
		if got != c.want {
			t.Errorf("Disposition(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestParseFallbackMode(t *testing.T) {
	cases := []struct {
		in      string
		want    FallbackMode
		wantErr bool
	}{
		{"", FallbackModePool, false},
		{"pool", FallbackModePool, false},
		{"generate", FallbackModeGenerate, false},
		{"both", FallbackModeBoth, false},
		{"POOL", 0, true},
		{"bogus", 0, true},
		{" pool", 0, true},
	}
	for _, c := range cases {
		got, err := ParseFallbackMode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseFallbackMode(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseFallbackMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFallbackSelect_GenerateMode_IgnoresPool(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeGenerate)

	// A .jpg key would match the pool, but generate mode must produce a
	// generated image and ignore the pool entirely.
	p := fb.Select("images/photo.jpg")
	if p == nil {
		t.Fatal("expected generated placeholder for .jpg")
	}
	if !p.Generated {
		t.Errorf("expected Generated=true under generate mode")
	}
	if p.Path != "" {
		t.Errorf("expected empty Path on generated placeholder, got %q", p.Path)
	}
}

func TestFallbackSelect_GenerateMode_PngWorks(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeGenerate)
	p := fb.Select("a/b.png")
	if p == nil || !p.Generated || p.ContentType != "image/png" {
		t.Fatalf("expected generated png, got %+v", p)
	}
}

func TestFallbackSelect_GenerateMode_UnsupportedExtNil(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeGenerate)
	// .pdf is in the pool, but generate mode doesn't support it.
	if p := fb.Select("docs/report.pdf"); p != nil {
		t.Errorf("expected nil under generate for .pdf, got %+v", p)
	}
	if p := fb.Select("data/export.csv"); p != nil {
		t.Errorf("expected nil under generate for .csv, got %+v", p)
	}
}

func TestFallbackSelect_BothMode_PoolFirst(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeBoth)
	p := fb.Select("images/photo.jpg")
	if p == nil {
		t.Fatal("expected placeholder under both mode")
	}
	if p.Generated {
		t.Errorf("expected pool placeholder first under both mode, got generated")
	}
	if !strings.HasSuffix(p.Path, ".jpg") {
		t.Errorf("expected jpg pool placeholder, got Path=%q", p.Path)
	}
}

func TestFallbackSelect_BothMode_GenerateFallback(t *testing.T) {
	// Empty pool dir → no pool matches; generate kicks in for supported
	// extensions.
	dir := t.TempDir()
	fb, _ := NewFallback(dir, DefaultInlineExtensions, FallbackModeBoth)

	p := fb.Select("a/b.png")
	if p == nil || !p.Generated {
		t.Fatalf("expected generated placeholder when pool is empty, got %+v", p)
	}

	if p := fb.Select("a/b.pdf"); p != nil {
		t.Errorf("expected nil for .pdf with empty pool and no generator support, got %+v", p)
	}
}

func TestFallbackGenerate_ETagMatchesMD5OfBody(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeGenerate)
	p := fb.Select("etag-check.png")
	if p == nil {
		t.Fatal("expected generated placeholder")
	}
	sum := md5.Sum(p.Body)
	want := `"` + hex.EncodeToString(sum[:]) + `"`
	if p.ETag != want {
		t.Errorf("ETag = %q, want %q", p.ETag, want)
	}
}

func TestFallbackLastModified_StableAcrossSelects(t *testing.T) {
	fb, _ := NewFallback("testdata/fallback", DefaultInlineExtensions, FallbackModeGenerate)
	t1 := fb.LastModified()
	_ = fb.Select("x.png")
	t2 := fb.LastModified()
	if !t1.Equal(t2) {
		t.Errorf("LastModified shifted between calls: %v vs %v", t1, t2)
	}
	if t1.IsZero() {
		t.Errorf("LastModified is zero value")
	}
}
