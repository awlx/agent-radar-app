package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// radarStatus is the status that the badge of the tray icon shows.
type radarStatus int

const (
	statusIdle    radarStatus = iota // yellow pause: no agent is active
	statusWorking                    // green check mark: agents work and no agent waits
	statusWaiting                    // red stop: an agent waits for your input
)

func (st trayStatus) radar() radarStatus {
	switch {
	case st.waiting > 0:
		return statusWaiting
	case st.working > 0:
		return statusWorking
	}
	return statusIdle
}

// iconBlips are the blip positions in the radar: the radius (part of the icon size) and the angle (degrees).
// They are the same as in the macOS menu bar icon.
var iconBlips = [][2]float64{{0.27, 35}, {0.16, 150}, {0.30, 245}, {0.22, 320}, {0.12, 85}, {0.31, 195}}

type rgba struct{ r, g, b, a float64 }

func over(dst, src rgba) rgba {
	a := src.a + dst.a*(1-src.a)
	if a == 0 {
		return rgba{}
	}
	mix := func(s, d float64) float64 { return (s*src.a + d*dst.a*(1-src.a)) / a }
	return rgba{mix(src.r, dst.r), mix(src.g, dst.g), mix(src.b, dst.b), a}
}

// segDist gives the distance from the point p to the line segment from a to b.
func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/(dx*dx+dy*dy)))
	return math.Hypot(px-ax-t*dx, py-ay-t*dy)
}

func inRect(x, y, x0, y0, x1, y1 float64) bool { return x >= x0 && x <= x1 && y >= y0 && y <= y1 }

// iconSample gives the color of the icon at the point (x, y) in the unit square. The y axis points down.
// ink is the color of the radar. Use a light ink on a dark taskbar.
func iconSample(st radarStatus, blips int, ink rgba, x, y float64) rgba {
	const cx, cy = 0.45, 0.45
	var c rgba
	d := math.Hypot(x-cx, y-cy)
	alpha := func(a float64) rgba { return rgba{ink.r, ink.g, ink.b, a} }
	if math.Abs(d-0.38) <= 0.045 {
		c = over(c, alpha(0.9))
	}
	if math.Abs(d-0.2) <= 0.025 {
		c = over(c, alpha(0.5))
	}
	if segDist(x, y, cx, cy, cx+0.26, cy-0.26) <= 0.04 {
		c = over(c, alpha(0.9))
	}
	for i := 0; i < blips && i < len(iconBlips); i++ {
		r, a := iconBlips[i][0], iconBlips[i][1]*math.Pi/180
		if math.Hypot(x-(cx+r*math.Cos(a)), y-(cy-r*math.Sin(a))) <= 0.065 {
			c = over(c, alpha(1))
		}
	}
	if d <= 0.05 {
		c = over(c, alpha(1))
	}

	// The badge in the lower-right corner, with a clear ring around it.
	const bx, by, br = 0.73, 0.73, 0.27
	bd := math.Hypot(x-bx, y-by)
	if bd <= br+0.06 {
		c = rgba{}
	}
	if bd > br {
		return c
	}
	white := rgba{1, 1, 1, 1}
	switch st {
	case statusWorking:
		c = rgba{0.20, 0.78, 0.35, 1}
		if segDist(x, y, bx-0.11, by+0.01, bx-0.03, by+0.09) <= 0.04 || segDist(x, y, bx-0.03, by+0.09, bx+0.12, by-0.09) <= 0.04 {
			c = white
		}
	case statusWaiting:
		c = rgba{1, 0.23, 0.19, 1}
		if inRect(x, y, bx-0.09, by-0.09, bx+0.09, by+0.09) {
			c = white
		}
	default:
		c = rgba{1, 0.8, 0, 1}
		if inRect(x, y, bx-0.095, by-0.11, bx-0.03, by+0.11) || inRect(x, y, bx+0.03, by-0.11, bx+0.095, by+0.11) {
			c = rgba{0.15, 0.15, 0.15, 1}
		}
	}
	return c
}

// iconImage draws the tray icon at size × size pixels, with 4 × 4 samples for each pixel.
func iconImage(st radarStatus, blips, size int, ink rgba) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := iconSample(st, blips, ink, (float64(px)+(float64(sx)+0.5)/ss)/float64(size), (float64(py)+(float64(sy)+0.5)/ss)/float64(size))
					r, g, b, a = r+c.r*c.a, g+c.g*c.a, b+c.b*c.a, a+c.a
				}
			}
			if a == 0 {
				continue
			}
			to8 := func(v float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
			img.SetNRGBA(px, py, color.NRGBA{to8(r / a), to8(g / a), to8(b / a), to8(a / ss / ss)})
		}
	}
	return img
}

func iconPNG(st radarStatus, blips, size int, ink rgba) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, iconImage(st, blips, size, ink))
	return buf.Bytes()
}

// icoSizes are the sizes in the ICO file. Windows selects the best size for the DPI of the screen.
var icoSizes = []int{16, 20, 24, 32, 40, 48, 64}

// iconICO makes an ICO file with one PNG image for each size in icoSizes.
func iconICO(st radarStatus, blips int, ink rgba) []byte {
	var head, body bytes.Buffer
	n := len(icoSizes)
	_ = binary.Write(&head, binary.LittleEndian, [3]uint16{0, 1, uint16(n)})
	offset := 6 + 16*n
	for _, s := range icoSizes {
		p := iconPNG(st, blips, s, ink)
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		_ = binary.Write(&head, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(p)), uint32(offset)})
		offset += len(p)
		body.Write(p)
	}
	return append(head.Bytes(), body.Bytes()...)
}
