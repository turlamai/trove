// genicon renders the Trove plugin icon (no external dependencies):
// an Unraid-orange rounded square with a white folder mark.
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}

func main() {
	const S = 64
	img := image.NewNRGBA(image.Rect(0, 0, S, S))

	// background: vertical orange gradient #ff8c2f → #f15a2c with rounded corners
	top := color.RGBA{255, 140, 47, 255}   // #ff8c2f
	bot := color.RGBA{241, 90, 44, 255}    // #f15a2c
	radius := 14.0
	for y := 0; y < S; y++ {
		t := float64(y) / float64(S-1)
		row := color.RGBA{lerp(top.R, bot.R, t), lerp(top.G, bot.G, t), lerp(top.B, bot.B, t), 255}
		for x := 0; x < S; x++ {
			// rounded-corner alpha mask
			cx := cornerDist(float64(x)+0.5, float64(y)+0.5, radius, S)
			if cx > 1 {
				img.Set(x, y, color.RGBA{0, 0, 0, 0})
			} else {
				img.Set(x, y, row)
			}
		}
	}

	// folder glyph (white): back panel + tab, front flap
	white := color.NRGBA{255, 255, 255, 255}
	drawFolder(img, white)

	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func cornerDist(x, y, r float64, size int) float64 {
	w := float64(size)
	// distance into the rounded-rect "anti" region; >1 means outside
	if x >= r && x <= w-r || y >= r && y <= w-r {
		return 0
	}
	px := clamp(x, r, w-r)
	py := clamp(y, r, w-r)
	d := dist(x, y, px, py)
	return d / r
}

func dist(x1, y1, x2, y2 float64) float64 {
	dx, dy := x1-x2, y1-y2
	// sqrt without math import churn — use math.Sqrt
	return sqrt(dx*dx + dy*dy)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func drawFolder(img *image.NRGBA, c color.NRGBA) {
	const S = 64
	set := func(x, y int) {
		if x >= 0 && y >= 0 && x < S && y < S {
			img.SetNRGBA(x, y, c)
		}
	}
	// tab (left-aligned): y 15..21, x 15..31 with rounded top corners
	for y := 15; y <= 21; y++ {
		x0, x1 := 15, 31
		if y == 15 {
			x0, x1 = 18, 28
		} else if y == 16 {
			x0, x1 = 16, 30
		}
		for x := x0; x <= x1; x++ {
			set(x, y)
		}
	}
	// body: y 21..47, x 12..52, rounded bottom corners
	for y := 21; y <= 47; y++ {
		x0, x1 := 12, 52
		if y == 47 {
			x0, x1 = 15, 49
		} else if y == 46 {
			x0, x1 = 13, 51
		}
		for x := x0; x <= x1; x++ {
			set(x, y)
		}
	}
	// inner notch line (background color) to separate flap: y 28, x 12..52
	bg := color.NRGBA{255, 255, 255, 0}
	for x := 13; x <= 51; x++ {
		img.SetNRGBA(x, 28, bg)
		img.SetNRGBA(x, 29, bg)
	}
}
