package main

import (
	"bytes"
	"crypto/md5"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"path"
	"strings"
)

const (
	canvasSize    = 512
	gridDivisions = 16
	cellSize      = canvasSize / gridDivisions
	minRadius     = 6.0
	maxRadius     = float64(canvasSize) / 2
	bubbleAlpha   = 191 // 0.75 * 255, rounded down — applied as src alpha for draw.Over
	bubbleCount   = 16
	jpegQuality   = 85
)

var generatableExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
}

// category20 is the D3 categorical 20-color palette, reproduced byte
// for byte. Bubble colors index into it as digest-derived
// `category20[(x+y) % 20]`.
var category20 = [20]color.NRGBA{
	{0x1f, 0x77, 0xb4, 0xff},
	{0xae, 0xc7, 0xe8, 0xff},
	{0xff, 0x7f, 0x0e, 0xff},
	{0xff, 0xbb, 0x78, 0xff},
	{0x2c, 0xa0, 0x2c, 0xff},
	{0x98, 0xdf, 0x8a, 0xff},
	{0xd6, 0x27, 0x28, 0xff},
	{0xff, 0x98, 0x96, 0xff},
	{0x94, 0x67, 0xbd, 0xff},
	{0xc5, 0xb0, 0xd5, 0xff},
	{0x8c, 0x56, 0x4b, 0xff},
	{0xc4, 0x9c, 0x94, 0xff},
	{0xe3, 0x77, 0xc2, 0xff},
	{0xf7, 0xb6, 0xd2, 0xff},
	{0x7f, 0x7f, 0x7f, 0xff},
	{0xc7, 0xc7, 0xc7, 0xff},
	{0xbc, 0xbd, 0x22, 0xff},
	{0xdb, 0xdb, 0x8d, 0xff},
	{0x17, 0xbe, 0xcf, 0xff},
	{0x9e, 0xda, 0xe5, 0xff},
}

type bubbleSpec struct {
	cx, cy int
	radius float64
	col    color.NRGBA
}

// bubbleFromDigest derives the i-th bubble (0..15) from the MD5 digest
// using the dakridge layout. Mirroring includes the (x*y)%32 radius
// quirk: bubbles whose digest byte has a zero nibble share radius
// indexing with bubble 0.
func bubbleFromDigest(digest [16]byte, i int) bubbleSpec {
	b := digest[i]
	x := int(b >> 4)
	y := int(b & 0x0F)
	idx := (x * y) % 32
	var nibble int
	if idx%2 == 0 {
		nibble = int(digest[idx/2] >> 4)
	} else {
		nibble = int(digest[idx/2] & 0x0F)
	}
	r := minRadius + (float64(nibble)/15.0)*(maxRadius-minRadius)
	return bubbleSpec{
		cx:     x * cellSize,
		cy:     y * cellSize,
		radius: r,
		col:    category20[(x+y)%20],
	}
}

// circleMask builds an anti-aliased disc mask of side 2*ceil(r). Each
// pixel's alpha is the fraction of 4×4 subsamples that fall inside the
// disc, scaled to 0..255.
//
// Only pixels straddling the disc edge actually need subsampling — for a
// large disc that's a thin ring, a tiny fraction of the total. Pixels
// whose entire area lies inside the disc are fully covered (alpha 255)
// and those entirely outside are empty (alpha 0), so we test the pixel
// center against the edge ± a half-pixel-diagonal guard band and run the
// 4×4 loop only for the ambiguous remainder. The guard band is the
// conservative full-diagonal (√2/2), wider than the subsample spread, so
// the result is byte-identical to subsampling every pixel — locked in by
// TestCircleMask_MatchesFullSupersample.
func circleMask(r float64) *image.Alpha {
	dim := int(math.Ceil(r)) * 2
	if dim < 1 {
		dim = 1
	}
	mask := image.NewAlpha(image.Rect(0, 0, dim, dim))
	center := float64(dim) / 2
	r2 := r * r
	const halfDiag = math.Sqrt2 / 2
	rIn := r - halfDiag
	rIn2 := rIn * rIn
	rOut := r + halfDiag
	rOut2 := rOut * rOut
	for py := 0; py < dim; py++ {
		for px := 0; px < dim; px++ {
			cx := float64(px) + 0.5 - center
			cy := float64(py) + 0.5 - center
			cd2 := cx*cx + cy*cy
			switch {
			case rIn > 0 && cd2 <= rIn2:
				mask.SetAlpha(px, py, color.Alpha{A: 255})
			case cd2 >= rOut2:
				// fully outside the disc — leave the zero value
			default:
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
	}
	return mask
}

func renderIdenticon(digest [16]byte) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, canvasSize, canvasSize))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.NRGBA{0xff, 0xff, 0xff, 0xff}}, image.Point{}, draw.Src)

	for i := 0; i < bubbleCount; i++ {
		b := bubbleFromDigest(digest, i)
		mask := circleMask(b.radius)
		offset := int(math.Ceil(b.radius))
		bubbleCol := color.NRGBA{b.col.R, b.col.G, b.col.B, bubbleAlpha}
		draw.DrawMask(canvas,
			image.Rect(b.cx-offset, b.cy-offset, b.cx+offset, b.cy+offset),
			&image.Uniform{bubbleCol}, image.Point{},
			mask, image.Point{}, draw.Over)
	}
	return canvas
}

// generateImage renders a key-seeded bubble identicon and encodes it
// in the format matching the key's extension. Returns (nil, "") when
// the extension is not generatable.
func generateImage(key string) (body []byte, contentType string) {
	ext := strings.ToLower(path.Ext(key))
	if !generatableExtensions[ext] {
		return nil, ""
	}
	digest := md5.Sum([]byte(key))
	canvas := renderIdenticon(digest)
	var buf bytes.Buffer
	switch ext {
	case ".png":
		if err := png.Encode(&buf, canvas); err != nil {
			return nil, ""
		}
		return buf.Bytes(), "image/png"
	case ".jpg", ".jpeg":
		if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, ""
		}
		return buf.Bytes(), "image/jpeg"
	}
	return nil, ""
}
