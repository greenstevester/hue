package client

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseColor converts a six-digit RGB colour to chromaticity and clips to the supplied gamut.
func ParseColor(value string, gamut *Gamut) (XY, error) {
	hex := strings.TrimPrefix(value, "#")
	if len(hex) != 6 {
		return XY{}, fmt.Errorf("colour must contain six hexadecimal digits")
	}
	if _, err := strconv.ParseUint(hex, 16, 24); err != nil {
		return XY{}, fmt.Errorf("invalid colour %q", value)
	}
	x, y := hexToXY(hex)
	return ClipXY(XY{X: x, Y: y}, gamut), nil
}

// ClipXY returns the nearest point inside the device's colour triangle.
func ClipXY(p XY, gamut *Gamut) XY {
	if gamut == nil {
		return p
	}
	vertices := []XY{gamut.Red, gamut.Green, gamut.Blue}
	cross := func(a, b, c XY) float64 { return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X) }
	if math.Abs(cross(vertices[0], vertices[1], vertices[2])) < 1e-12 {
		return p
	}
	a, b, c := cross(vertices[0], vertices[1], p), cross(vertices[1], vertices[2], p), cross(vertices[2], vertices[0], p)
	if (a >= 0 && b >= 0 && c >= 0) || (a <= 0 && b <= 0 && c <= 0) {
		return p
	}
	best, distance := p, math.Inf(1)
	for i, a := range vertices {
		b := vertices[(i+1)%3]
		dx, dy := b.X-a.X, b.Y-a.Y
		length := dx*dx + dy*dy
		if length == 0 {
			continue
		}
		t := math.Max(0, math.Min(1, ((p.X-a.X)*dx+(p.Y-a.Y)*dy)/length))
		q := XY{X: a.X + t*dx, Y: a.Y + t*dy}
		d := math.Hypot(p.X-q.X, p.Y-q.Y)
		if d < distance {
			best, distance = q, d
		}
	}
	return best
}
