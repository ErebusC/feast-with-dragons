package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
)

const (
	coverW = 1200
	coverH = 1800
)

// generateCover blends two cover images side-by-side with a smooth horizontal
// fade between them, adds top and bottom gradient overlays for text legibility,
// and returns the result as a JPEG-encoded byte slice. Accepts JPEG or PNG
// input for both images.
//
// The caller (cover.html) is responsible for rendering the title text over
// the image via CSS, so no text is burned into the image itself.
// decodeImage tries JPEG first, then PNG. Returns the decoded image or an
// error if neither format matches.
func decodeImage(data []byte) (image.Image, error) {
	if img, err := jpeg.Decode(bytes.NewReader(data)); err == nil {
		return img, nil
	}
	if img, err := png.Decode(bytes.NewReader(data)); err == nil {
		return img, nil
	}
	return nil, fmt.Errorf("cover image is not JPEG or PNG")
}

func generateCover(leftJPEG, rightJPEG []byte) ([]byte, error) {
	leftSrc, err := decodeImage(leftJPEG)
	if err != nil {
		return nil, fmt.Errorf("left cover: %w", err)
	}
	rightSrc, err := decodeImage(rightJPEG)
	if err != nil {
		return nil, fmt.Errorf("right cover: %w", err)
	}

	leftR := bilinearResize(leftSrc, coverW, coverH)
	rightR := bilinearResize(rightSrc, coverW, coverH)

	// Build blended canvas pixel by pixel using a smoothstep fade.
	// Left image is fully visible from x=0 to x=fadeStart*W,
	// right image is fully visible from x=fadeEnd*W onwards.
	const fadeStart = 0.30
	const fadeEnd = 0.70

	out := image.NewRGBA(image.Rect(0, 0, coverW, coverH))
	for y := 0; y < coverH; y++ {
		for x := 0; x < coverW; x++ {
			t := float64(x) / float64(coverW)

			var alpha float64 // alpha = weight of left image (1.0 = full left, 0.0 = full right)
			if t <= fadeStart {
				alpha = 1.0
			} else if t >= fadeEnd {
				alpha = 0.0
			} else {
				s := (t - fadeStart) / (fadeEnd - fadeStart)
				alpha = 1.0 - (3*s*s - 2*s*s*s) // smoothstep
			}

			ar, ag, ab, _ := leftR.At(x, y).RGBA()
			br, bg, bb, _ := rightR.At(x, y).RGBA()

			// RGBA() returns 16-bit values; scale back to 8-bit
			r := uint8((float64(ar>>8)*alpha + float64(br>>8)*(1-alpha)))
			g := uint8((float64(ag>>8)*alpha + float64(bg>>8)*(1-alpha)))
			b := uint8((float64(ab>>8)*alpha + float64(bb>>8)*(1-alpha)))
			out.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}

	// Dark gradient overlay — top band (title area)
	topH := int(float64(coverH) * 0.38)
	for y := 0; y < topH; y++ {
		frac := float64(y) / float64(topH)
		alpha := uint8(210.0 * math.Pow(1.0-frac, 1.5))
		applyHLine(out, y, coverW, alpha)
	}

	// Dark gradient overlay — bottom band (author name area)
	botH := int(float64(coverH) * 0.20)
	for y := 0; y < botH; y++ {
		frac := float64(y) / float64(botH)
		alpha := uint8(190.0 * math.Pow(frac, 1.5))
		applyHLine(out, coverH-botH+y, coverW, alpha)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// applyHLine darkens a horizontal line of pixels by blending with black at
// the given alpha level (0 = no change, 255 = fully black).
func applyHLine(img *image.RGBA, y, w int, alpha uint8) {
	a := float64(alpha) / 255.0
	for x := 0; x < w; x++ {
		c := img.RGBAAt(x, y)
		img.SetRGBA(x, y, color.RGBA{
			R: uint8(float64(c.R) * (1 - a)),
			G: uint8(float64(c.G) * (1 - a)),
			B: uint8(float64(c.B) * (1 - a)),
			A: 255,
		})
	}
}

// bilinearResize scales src to the given dimensions using bilinear
// interpolation. This avoids pulling in golang.org/x/image.
func bilinearResize(src image.Image, w, h int) image.Image {
	srcB := src.Bounds()
	sw := float64(srcB.Max.X - srcB.Min.X)
	sh := float64(srcB.Max.Y - srcB.Min.Y)

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.Black, image.Point{}, draw.Src)

	xScale := sw / float64(w)
	yScale := sh / float64(h)

	for dy := 0; dy < h; dy++ {
		srcY := float64(dy)*yScale + float64(srcB.Min.Y)
		y0 := int(srcY)
		y1 := y0 + 1
		yf := srcY - float64(y0)
		if y1 >= srcB.Max.Y {
			y1 = srcB.Max.Y - 1
		}

		for dx := 0; dx < w; dx++ {
			srcX := float64(dx)*xScale + float64(srcB.Min.X)
			x0 := int(srcX)
			x1 := x0 + 1
			xf := srcX - float64(x0)
			if x1 >= srcB.Max.X {
				x1 = srcB.Max.X - 1
			}

			// Sample four neighbours
			c00r, c00g, c00b, _ := src.At(x0, y0).RGBA()
			c10r, c10g, c10b, _ := src.At(x1, y0).RGBA()
			c01r, c01g, c01b, _ := src.At(x0, y1).RGBA()
			c11r, c11g, c11b, _ := src.At(x1, y1).RGBA()

			lerp := func(a, b uint32, t float64) uint8 {
				return uint8(((float64(a>>8))*(1-t) + (float64(b>>8))*t))
			}

			r := uint8(float64(lerp(c00r, c10r, xf))*(1-yf) + float64(lerp(c01r, c11r, xf))*yf)
			g := uint8(float64(lerp(c00g, c10g, xf))*(1-yf) + float64(lerp(c01g, c11g, xf))*yf)
			b := uint8(float64(lerp(c00b, c10b, xf))*(1-yf) + float64(lerp(c01b, c11b, xf))*yf)

			dst.SetRGBA(dx, dy, color.RGBA{r, g, b, 255})
		}
	}
	return dst
}
