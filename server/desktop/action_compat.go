package desktop

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
)

// TypePasteRuneThreshold is the auto-paste cutoff for the type action.
// Shorter ASCII text still uses xdotool type --delay 12; longer, multiline,
// or non-ASCII text is injected via the X11 clipboard and ctrl+v.
const TypePasteRuneThreshold = 48

func isIntegerFloat(v float64) bool {
	_, frac := math.Modf(math.Abs(v))
	return frac < 1e-9 || frac > 1-1e-9
}

// CoordIsNormalized decides whether (x, y) should be treated as [0,1]
// viewport fractions or as absolute pixels.
//
// Priority:
//  1. coord_mode: "normalized" / "pixel" (and common aliases)
//  2. explicit normalized boolean
//  3. auto: both values in [0, 1] AND at least one is non-integer
//     (so 0.5,0.5 is normalized; 0,0 / 1,1 / 1.0,1.0 are pixels)
func CoordIsNormalized(x, y float64, coordMode string, normalized *bool) bool {
	switch strings.ToLower(strings.TrimSpace(coordMode)) {
	case "normalized", "norm", "relative", "ratio":
		return true
	case "pixel", "pixels", "absolute", "abs", "px":
		return false
	}
	if normalized != nil {
		return *normalized
	}
	if x < 0 || y < 0 || x > 1.0 || y > 1.0 {
		return false
	}
	return !isIntegerFloat(x) || !isIntegerFloat(y)
}

// ApplyActionAliases copies documented protocol aliases onto the canonical fields
// without overwriting values the caller already set.
func (a *AgentAction) ApplyActionAliases() {
	if a == nil {
		return
	}
	if a.Count <= 0 && a.Clicks > 0 {
		a.Count = a.Clicks
	}
	if strings.TrimSpace(a.Key) == "" && len(a.Keys) > 0 {
		parts := make([]string, 0, len(a.Keys))
		for _, k := range a.Keys {
			if t := strings.TrimSpace(k); t != "" {
				parts = append(parts, t)
			}
		}
		a.Key = strings.Join(parts, "+")
	}
	if a.FromX == nil && a.X != nil {
		a.FromX = a.X
	}
	if a.FromY == nil && a.Y != nil {
		a.FromY = a.Y
	}
	dir := strings.ToLower(strings.TrimSpace(a.Direction))
	amt := a.Amount
	if amt <= 0 {
		amt = 1
	}
	switch dir {
	case "up":
		if a.Dy == 0 {
			a.Dy = -amt
		}
	case "down":
		if a.Dy == 0 {
			a.Dy = amt
		}
	case "left":
		if a.Dx == 0 {
			a.Dx = -amt
		}
	case "right":
		if a.Dx == 0 {
			a.Dx = amt
		}
	}
	if a.Count <= 0 && a.Amount > 0 {
		a.Count = a.Amount
	}
}

// ShouldPasteText reports whether a type action should use clipboard paste.
// mode "paste"/"clipboard" always pastes; "type"/"xdotool" always types;
// auto (default) pastes for multiline, non-ASCII, or long text.
func ShouldPasteText(text, mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "paste", "clipboard":
		return true
	case "type", "xdotool":
		return false
	}
	if strings.ContainsAny(text, "\n\r") {
		return true
	}
	runes := []rune(text)
	for _, r := range runes {
		if r > 127 {
			return true
		}
	}
	return len(runes) >= TypePasteRuneThreshold
}

func resizeImageBytes(src []byte, format string, scale float64) ([]byte, string, error) {
	if scale <= 0 || math.Abs(scale-1.0) < 1e-9 || len(src) == 0 {
		return src, format, nil
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return src, format, err
	}
	b := img.Bounds()
	tw := int(math.Max(1, math.Round(float64(b.Dx())*scale)))
	th := int(math.Max(1, math.Round(float64(b.Dy())*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			sx := b.Min.X + x*b.Dx()/tw
			sy := b.Min.Y + y*b.Dy()/th
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	outFormat := strings.ToLower(strings.TrimSpace(format))
	if outFormat == "jpg" {
		outFormat = "jpeg"
	}
	if outFormat == "jpeg" {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
			return src, format, err
		}
		return buf.Bytes(), "jpeg", nil
	}
	if err := png.Encode(&buf, dst); err != nil {
		return src, format, err
	}
	return buf.Bytes(), "png", nil
}

func scaledDimensions(width, height int, scale float64) (int, int) {
	if scale <= 0 || math.Abs(scale-1.0) < 1e-9 {
		return width, height
	}
	w := int(math.Max(1, math.Round(float64(width)*scale)))
	h := int(math.Max(1, math.Round(float64(height)*scale)))
	return w, h
}
