package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestGenerateImage_SupportedExtensions(t *testing.T) {
	cases := []struct {
		key      string
		wantType string
	}{
		{"photos/a.png", "image/png"},
		{"photos/b.jpg", "image/jpeg"},
		{"photos/c.jpeg", "image/jpeg"},
		{"nested/dir/x.PNG", "image/png"},
		{"upper/Y.JPG", "image/jpeg"},
	}
	for _, c := range cases {
		body, ct := generateImage(c.key)
		if body == nil {
			t.Errorf("generateImage(%q) returned nil body", c.key)
			continue
		}
		if ct != c.wantType {
			t.Errorf("generateImage(%q) content-type = %q, want %q", c.key, ct, c.wantType)
		}
	}
}

func TestGenerateImage_UnsupportedExtensions(t *testing.T) {
	cases := []string{
		"a.gif",
		"b.pdf",
		"c.txt",
		"d.webp",
		"no-extension",
		"trailing.dot.",
		"",
	}
	for _, key := range cases {
		body, ct := generateImage(key)
		if body != nil {
			t.Errorf("generateImage(%q) returned non-nil body, want nil", key)
		}
		if ct != "" {
			t.Errorf("generateImage(%q) content-type = %q, want empty", key, ct)
		}
	}
}

func TestGenerateImage_Deterministic(t *testing.T) {
	keys := []string{"a.png", "deeper/nested/key.jpg", "Δ-unicode.jpeg"}
	for _, k := range keys {
		body1, _ := generateImage(k)
		body2, _ := generateImage(k)
		if !bytes.Equal(body1, body2) {
			t.Errorf("generateImage(%q) is non-deterministic", k)
		}
	}
}

func TestGenerateImage_DifferentKeysDifferBytes(t *testing.T) {
	a, _ := generateImage("key-a.png")
	b, _ := generateImage("key-b.png")
	if bytes.Equal(a, b) {
		t.Errorf("two different keys produced identical bodies")
	}
}

func TestGenerateImage_PNGDecodesToCanvasSize(t *testing.T) {
	body, _ := generateImage("sample.png")
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != canvasSize || bounds.Dy() != canvasSize {
		t.Errorf("PNG dimensions = %dx%d, want %dx%d",
			bounds.Dx(), bounds.Dy(), canvasSize, canvasSize)
	}
}

func TestGenerateImage_JPEGDecodesToCanvasSize(t *testing.T) {
	body, _ := generateImage("sample.jpg")
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != canvasSize || bounds.Dy() != canvasSize {
		t.Errorf("JPEG dimensions = %dx%d, want %dx%d",
			bounds.Dx(), bounds.Dy(), canvasSize, canvasSize)
	}
}

func TestBubbleFromDigest_AllZero(t *testing.T) {
	digest := [16]byte{}
	got := bubbleFromDigest(digest, 0)
	want := bubbleSpec{
		cx: 0, cy: 0,
		radius: minRadius,
		col:    category20[0],
	}
	if got != want {
		t.Errorf("bubbleFromDigest(zeros, 0) = %+v, want %+v", got, want)
	}
}

func TestBubbleFromDigest_AllFF(t *testing.T) {
	var digest [16]byte
	for i := range digest {
		digest[i] = 0xff
	}
	got := bubbleFromDigest(digest, 0)
	want := bubbleSpec{
		cx: 15 * cellSize, cy: 15 * cellSize,
		radius: maxRadius,
		col:    category20[(15+15)%20],
	}
	if got != want {
		t.Errorf("bubbleFromDigest(0xff, 0) = %+v, want %+v", got, want)
	}
}

func TestBubbleFromDigest_EvenIdxBranch(t *testing.T) {
	// digest[0]=0x12 → x=1, y=2, cx=32, cy=64, idx=(1*2)%32=2 (even)
	// digest[1]=0x34 → high nibble = 3 → n=3
	// r = 6 + (3/15)*(256-6) = 56
	// col = category20[(1+2)%20] = category20[3]
	digest := [16]byte{0x12, 0x34}
	got := bubbleFromDigest(digest, 0)
	want := bubbleSpec{
		cx: 32, cy: 64,
		radius: 56.0,
		col:    category20[3],
	}
	if got != want {
		t.Errorf("bubbleFromDigest(0x12 0x34, 0) = %+v, want %+v", got, want)
	}
}

func TestBubbleFromDigest_OddIdxBranch(t *testing.T) {
	// digest[0]=0x35 → x=3, y=5, cx=96, cy=160, idx=(3*5)%32=15 (odd)
	// idx/2=7, so read low nibble of digest[7]=0x4a → n=10
	// r = 6 + (10/15)*250 ≈ 172.666...
	// col = category20[(3+5)%20] = category20[8]
	digest := [16]byte{0x35, 0, 0, 0, 0, 0, 0, 0x4a}
	got := bubbleFromDigest(digest, 0)
	wantR := minRadius + (10.0/15.0)*(maxRadius-minRadius)
	want := bubbleSpec{
		cx: 96, cy: 160,
		radius: wantR,
		col:    category20[8],
	}
	if got != want {
		t.Errorf("bubbleFromDigest(odd-idx fixture, 0) = %+v, want %+v", got, want)
	}
}

func TestCircleMask_Dimensions(t *testing.T) {
	cases := []float64{1, 5.0, 5.7, 100, 256}
	for _, r := range cases {
		m := circleMask(r)
		want := int(ceilFloat(r)) * 2
		if want < 1 {
			want = 1
		}
		if m.Bounds().Dx() != want || m.Bounds().Dy() != want {
			t.Errorf("circleMask(%v).Bounds = %v, want %dx%d", r, m.Bounds(), want, want)
		}
	}
}

func TestCircleMask_CenterFullyCovered(t *testing.T) {
	m := circleMask(100)
	// dim = 200, center pixel index = (100, 100). Subsamples around (100.5, 100.5),
	// distance to center (100, 100) max ≈ 0.7, well within radius 100.
	got := m.AlphaAt(100, 100).A
	if got != 255 {
		t.Errorf("center pixel alpha = %d, want 255", got)
	}
}

func TestCircleMask_FarCornerEmpty(t *testing.T) {
	m := circleMask(100)
	got := m.AlphaAt(0, 0).A
	if got != 0 {
		t.Errorf("far-corner pixel alpha = %d, want 0", got)
	}
	got = m.AlphaAt(199, 199).A
	if got != 0 {
		t.Errorf("opposite far-corner alpha = %d, want 0", got)
	}
}

func TestCircleMask_BoundaryHasIntermediateAlpha(t *testing.T) {
	m := circleMask(100)
	// The disc edge sweeps through the mask; somewhere on the boundary,
	// a pixel should have partial coverage.
	intermediate := 0
	for y := 0; y < m.Bounds().Dy(); y++ {
		for x := 0; x < m.Bounds().Dx(); x++ {
			a := m.AlphaAt(x, y).A
			if a > 0 && a < 255 {
				intermediate++
			}
		}
	}
	if intermediate == 0 {
		t.Errorf("found no anti-aliased boundary pixels — subsampling is not wired up")
	}
}

func TestRenderIdenticon_NotUniformlyWhite(t *testing.T) {
	digest := [16]byte{0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89,
		0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	canvas := renderIdenticon(digest)
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	off := 0
	bounds := canvas.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := canvas.NRGBAAt(x, y)
			if c != white {
				off++
			}
		}
	}
	if off == 0 {
		t.Errorf("canvas is uniformly white — no bubbles drawn")
	}
}

// referenceCircleMask is the original full-supersample implementation,
// kept here as the correctness oracle for the optimized circleMask:
// every pixel runs the 4×4 subsample loop. The production circleMask
// skips that loop for pixels wholly inside/outside the disc, and must
// produce byte-identical output.
func referenceCircleMask(r float64) *image.Alpha {
	dim := int(ceilFloat(r)) * 2
	if dim < 1 {
		dim = 1
	}
	mask := image.NewAlpha(image.Rect(0, 0, dim, dim))
	center := float64(dim) / 2
	r2 := r * r
	for py := 0; py < dim; py++ {
		for px := 0; px < dim; px++ {
			count := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					fx := float64(px) + (float64(sx)+0.5)/4
					fy := float64(py) + (float64(sy)+0.5)/4
					dx := fx - center
					dy := fy - center
					if dx*dx+dy*dy <= r2 {
						count++
					}
				}
			}
			mask.SetAlpha(px, py, color.Alpha{A: uint8(count * 255 / 16)})
		}
	}
	return mask
}

func TestCircleMask_MatchesFullSupersample(t *testing.T) {
	// Cover small/large/fractional radii, including the bubble radius
	// bounds (minRadius, maxRadius) the generator actually produces.
	for _, r := range []float64{1, 5, 5.7, 6, 7.3, 50, 99.9, 100, 131, 200, 256} {
		got := circleMask(r)
		want := referenceCircleMask(r)
		if got.Rect != want.Rect {
			t.Fatalf("r=%v rect = %v, want %v", r, got.Rect, want.Rect)
		}
		for i := range want.Pix {
			if got.Pix[i] != want.Pix[i] {
				t.Fatalf("r=%v pixel %d alpha = %d, want %d", r, i, got.Pix[i], want.Pix[i])
			}
		}
	}
}

func ceilFloat(f float64) int {
	i := int(f)
	if float64(i) < f {
		return i + 1
	}
	return i
}
