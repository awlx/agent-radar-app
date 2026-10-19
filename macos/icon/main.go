// Command icon writes the PNG files of the app icon to the folder that you give.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const ss = 4 // samples on each axis for each pixel

type rgba struct{ r, g, b, a float64 }

func over(dst, src rgba) rgba {
	a := src.a + dst.a*(1-src.a)
	if a == 0 {
		return rgba{}
	}
	mix := func(s, d float64) float64 { return (s*src.a + d*dst.a*(1-src.a)) / a }
	return rgba{mix(src.r, dst.r), mix(src.g, dst.g), mix(src.b, dst.b), a}
}

// sample gives the color at the point (x, y) in the unit square.
func sample(x, y float64) rgba {
	// Rounded square with the macOS margin (about 10 % on each side).
	const m, rad = 0.1, 0.18
	qx, qy := math.Max(math.Abs(x-0.5)-(0.5-m-rad), 0), math.Max(math.Abs(y-0.5)-(0.5-m-rad), 0)
	if math.Hypot(qx, qy) > rad {
		return rgba{}
	}
	c := rgba{0.05, 0.07, 0.09, 1}
	dx, dy := x-0.5, y-0.5
	d := math.Hypot(dx, dy)
	ang := math.Atan2(-dy, dx)
	green := rgba{0.25, 0.85, 0.55, 1}
	// Sweep: a wedge that fades behind the beam at 45 degrees.
	if d < 0.36 {
		beam := math.Pi / 4
		behind := math.Mod(beam-ang+4*math.Pi, 2*math.Pi)
		if behind < math.Pi/2 {
			c = over(c, rgba{green.r, green.g, green.b, 0.45 * (1 - behind/(math.Pi/2))})
		}
	}
	for _, r := range []float64{0.12, 0.24, 0.36} {
		if math.Abs(d-r) < 0.008 {
			c = over(c, rgba{green.r, green.g, green.b, 0.7})
		}
	}
	for _, p := range [][3]float64{{0.62, 0.36, 0.035}, {0.33, 0.6, 0.03}, {0.66, 0.68, 0.025}, {0.5, 0.5, 0.03}} {
		if math.Hypot(x-p[0], y-p[1]) < p[2] {
			c = over(c, rgba{0.55, 0.95, 1, 1})
		}
	}
	return c
}

func render(n int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					c := sample((float64(px)+(float64(sx)+0.5)/ss)/float64(n), (float64(py)+(float64(sy)+0.5)/ss)/float64(n))
					r, g, b, a = r+c.r*c.a, g+c.g*c.a, b+c.b*c.a, a+c.a
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{uint8(255 * r / a), uint8(255 * g / a), uint8(255 * b / a), uint8(255 * a / (ss * ss))})
		}
	}
	return img
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: icon <AppIcon.iconset>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	for _, s := range []int{16, 32, 128, 256, 512} {
		for _, k := range []int{1, 2} {
			name := fmt.Sprintf("icon_%dx%d.png", s, s)
			if k == 2 {
				name = fmt.Sprintf("icon_%dx%d@2x.png", s, s)
			}
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				panic(err)
			}
			if err := png.Encode(f, render(s*k)); err != nil {
				panic(err)
			}
			f.Close()
		}
	}
}
