package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// IconState is everything the tray icon shows.
type IconState struct {
	Queued   int  // PR/MRs waiting for me
	Urgent   int  // urgent ones
	Decide   int  // comment proposals waiting for my decision
	Running  bool // a review is in progress
	Problem  bool // poll error or doctor failure
	Flash    bool // urgent animation is on: draw the bolt
	Ask      bool // a proposal waits for me: the magnifier stays, Linux gets a blinking ? badge
	Frame    int  // animation frame while Flash (sparks move, bolt pulses)
	Template bool // macOS template image: black + alpha, no badge digits
	Badge    bool // draw the count in a badge (Linux, where titles are unreliable)
	Sleep    bool // off hours: the magnifier sleeps in bed, nothing else shows
}

const iconPx = 32

var (
	red    = color.NRGBA{0xE5, 0x39, 0x35, 0xFF}
	volt   = color.NRGBA{0xFF, 0xD4, 0x00, 0xFF}
	voltHi = color.NRGBA{0xFF, 0xF3, 0xB0, 0xFF}
	ink    = color.NRGBA{0x2B, 0x1D, 0x00, 0xE6}
	blue   = color.NRGBA{0x2F, 0x81, 0xF7, 0xFF}
	gray   = color.NRGBA{0x9A, 0xA0, 0xA6, 0xFF}
	amber  = color.NRGBA{0xF5, 0xA6, 0x23, 0xFF}
	black  = color.NRGBA{0, 0, 0, 0xFF}
	white  = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	shadow = color.NRGBA{0, 0, 0, 0x99}
)

// shape returns coverage in [0,1] at (x, y) in a 1x1 unit square.
type shape func(x, y float64) float64

func ring(cx, cy, r, w float64) shape {
	return func(x, y float64) float64 {
		return inside(math.Abs(math.Hypot(x-cx, y-cy)-r) - w/2)
	}
}

func disc(cx, cy, r float64) shape {
	return func(x, y float64) float64 { return inside(math.Hypot(x-cx, y-cy) - r) }
}

func segment(x1, y1, x2, y2, w float64) shape {
	return func(x, y float64) float64 {
		dx, dy := x2-x1, y2-y1
		t := ((x-x1)*dx + (y-y1)*dy) / (dx*dx + dy*dy)
		t = math.Max(0, math.Min(1, t))
		return inside(math.Hypot(x-(x1+t*dx), y-(y1+t*dy)) - w/2)
	}
}

func inside(d float64) float64 {
	if d <= 0 {
		return 1
	}
	return 0
}

type layer struct {
	s shape
	c color.NRGBA
}

// render supersamples layers (painted in order) into a PNG.
func render(layers []layer) []byte {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, iconPx, iconPx))
	for py := 0; py < iconPx; py++ {
		for px := 0; px < iconPx; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / iconPx
					y := (float64(py) + (float64(sy)+0.5)/ss) / iconPx
					cr, cg, cb, ca := 0.0, 0.0, 0.0, 0.0
					for _, l := range layers {
						if cov := l.s(x, y); cov > 0 {
							la := float64(l.c.A) / 255 * cov
							cr = float64(l.c.R)*la + cr*(1-la)
							cg = float64(l.c.G)*la + cg*(1-la)
							cb = float64(l.c.B)*la + cb*(1-la)
							ca = la + ca*(1-la)
						}
					}
					r, g, b, a = r+cr, g+cg, b+cb, a+ca
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(math.Round(r / a)), G: uint8(math.Round(g / a)), B: uint8(math.Round(b / a)),
					A: uint8(math.Round(a / n * 255)),
				})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Render draws the icon: a magnifier (review), plus a status mark; while an
// urgent item waits it becomes a lightning bolt throwing sparks.
func Render(s IconState) []byte {
	if s.Sleep {
		return render(sleeping(s))
	}
	if s.Flash {
		return render(zap(s))
	}
	glyph := gray
	switch {
	case s.Template:
		glyph = black
	case s.Flash:
		glyph = red
	case s.Queued > 0:
		glyph = blue
	}
	if s.Template && s.Flash {
		// flashing on macOS: leave template mode for a red glyph
		glyph = red
	}
	var ls []layer
	lens := ring(0.40, 0.40, 0.25, 0.11)
	handle := segment(0.58, 0.58, 0.86, 0.86, 0.14)
	ls = append(ls, layer{lens, glyph}, layer{handle, glyph})

	switch {
	case s.Badge && s.Ask:
		ls = append(ls, askBadge(s)...)
	case s.Badge && (s.Queued > 0 || s.Urgent > 0):
		n := s.Queued
		bg := blue
		if s.Urgent > 0 && s.Flash {
			n, bg = s.Urgent, red
		}
		ls = append(ls, badge(n, bg)...)
	case s.Problem:
		c := amber
		if s.Template && !s.Flash {
			c = black
		}
		ls = append(ls, layer{disc(0.40, 0.40, 0.11), c})
	case s.Running:
		ls = append(ls, layer{ring(0.40, 0.40, 0.08, 0.06), glyph})
	case s.Queued > 0:
		ls = append(ls, layer{disc(0.40, 0.40, 0.09), glyph})
	}
	return render(ls)
}

// badge is a filled circle at the bottom right with n (9+ above nine).
func badge(n int, bg color.NRGBA) []layer { return badgeAt(n, bg, 0.70, 0.70, 0.30, 0.055) }

func badgeAt(n int, bg color.NRGBA, cx, cy, r, cell float64) []layer {
	ls := []layer{{disc(cx, cy, r+0.03), shadow}, {disc(cx, cy, r), bg}}
	text := []rune{'0' + rune(min(n, 9))}
	if n > 9 {
		text = []rune{'9', '+'}
	}
	w := float64(len(text)*4-1) * cell
	x0, y0 := cx-w/2, cy-2.5*cell
	for i, ch := range text {
		bm := digits[ch]
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if bm[row][col] == '#' {
					x := x0 + float64(i*4+col)*cell
					y := y0 + float64(row)*cell
					ls = append(ls, layer{rect(x, y, cell, cell), white})
				}
			}
		}
	}
	return ls
}

func rect(x, y, w, h float64) shape {
	return func(px, py float64) float64 {
		if px >= x && px < x+w && py >= y && py < y+h {
			return 1
		}
		return 0
	}
}

var digits = map[rune][5]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {".#.", "##.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", ".#.", ".#.", ".#."},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	'+': {"...", ".#.", "###", ".#.", "..."},
}

// polygon fills a simple polygon (even-odd rule).
func polygon(pts ...[2]float64) shape {
	return func(x, y float64) float64 {
		in := false
		for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
			xi, yi, xj, yj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
			if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
		if in {
			return 1
		}
		return 0
	}
}

// boltPts is a lightning bolt in the unit square.
var boltPts = [][2]float64{{0.58, 0.04}, {0.20, 0.56}, {0.45, 0.56}, {0.36, 0.96}, {0.80, 0.40}, {0.54, 0.40}, {0.68, 0.04}}

// sparkSets are where the sparks fly in successive frames.
var sparkSets = [][][4]float64{ // x, y, angle (rad), length
	{{0.16, 0.20, 3.9, 0.12}, {0.86, 0.66, 0.5, 0.10}, {0.12, 0.84, 2.4, 0.08}},
	{{0.84, 0.18, 5.5, 0.12}, {0.18, 0.66, 2.9, 0.10}, {0.74, 0.92, 0.9, 0.08}},
	{{0.10, 0.40, 3.3, 0.10}, {0.90, 0.44, 6.0, 0.12}, {0.50, 0.98, 1.6, 0.06}},
	{{0.30, 0.10, 4.4, 0.10}, {0.92, 0.84, 0.7, 0.10}, {0.08, 0.62, 2.7, 0.10}},
}

func zap(s IconState) []layer {
	pts := make([][2]float64, len(boltPts))
	copy(pts, boltPts)
	fill := volt
	if s.Frame%2 == 1 {
		fill = voltHi // pulse
	}
	// outline: the same bolt scaled up around its centre, in dark ink, so
	// it reads on light and dark bars
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{0.5 + (p[0]-0.5)*1.16, 0.5 + (p[1]-0.5)*1.10}
	}
	ls := []layer{{polygon(out...), ink}, {polygon(pts...), fill}}
	for _, sp := range sparkSets[s.Frame%len(sparkSets)] {
		x, y, a, l := sp[0], sp[1], sp[2], sp[3]
		x2, y2 := x+l*math.Cos(a), y+l*math.Sin(a)
		ls = append(ls, layer{segment(x, y, x2, y2, 0.07), ink}, layer{segment(x, y, x2, y2, 0.04), fill}, layer{disc(x, y, 0.035), white})
	}
	if s.Badge && s.Urgent > 0 {
		ls = append(ls, badgeAt(s.Urgent, red, 0.80, 0.80, 0.20, 0.04)...) // small: keep the bolt visible
	}
	return ls
}

// arc is the part of a ring between angles a1..a2 (radians, clockwise from
// +x, y pointing down).
func arc(cx, cy, r, w, a1, a2 float64) shape {
	ringS := ring(cx, cy, r, w)
	return func(x, y float64) float64 {
		a := math.Atan2(y-cy, x-cx)
		for a < a1 {
			a += 2 * math.Pi
		}
		if a > a2 {
			return 0
		}
		return ringS(x, y)
	}
}

var (
	violet   = color.NRGBA{0xA3, 0x71, 0xF7, 0xFF}
	violetHi = color.NRGBA{0xE2, 0xD4, 0xFF, 0xFF}
)

// askBadge (Linux, where the title is unreliable) is a blinking violet badge
// with a question mark on the magnifier: a proposal waits for my decision.
func askBadge(s IconState) []layer {
	cx, cy, r := 0.70, 0.70, 0.30
	bg, fg := violet, white
	if s.Frame%2 == 1 {
		bg, fg = violetHi, ink
	}
	return []layer{{disc(cx, cy, r+0.03), shadow}, {disc(cx, cy, r), bg},
		{arc(cx, cy-0.07, 0.09, 0.06, -math.Pi, math.Pi/2), fg},
		{segment(cx, cy+0.02, cx, cy+0.07, 0.06), fg},
		{disc(cx, cy+0.155, 0.035), fg}}
}

// sleeping is the off-hours icon: the magnifier lying in a bed, tucked in
// up to the lens, with a "z" above it.
func sleeping(s IconState) []layer {
	c := gray
	if s.Template {
		c = black
	}
	return []layer{
		{segment(0.08, 0.40, 0.08, 0.92, 0.09), c}, // headboard
		{segment(0.08, 0.80, 0.94, 0.80, 0.08), c}, // frame
		{segment(0.92, 0.66, 0.92, 0.92, 0.08), c}, // footboard
		{ring(0.25, 0.56, 0.125, 0.07), c},         // the lens on the pillow
		{segment(0.34, 0.65, 0.46, 0.66, 0.09), c}, // its handle, going under the blanket
		{rect(0.44, 0.56, 0.48, 0.20), c},          // the blanket over the handle
		{segment(0.58, 0.10, 0.80, 0.10, 0.06), c}, // z
		{segment(0.80, 0.10, 0.58, 0.34, 0.06), c},
		{segment(0.58, 0.34, 0.80, 0.34, 0.06), c},
	}
}
