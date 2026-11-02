package main

import (
	"bytes"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
	"strconv"
	"strings"
)

// The device screen is 320x240. Everything drawn for it goes through one Canvas, so the PNG preview
// in the web page and the bytes written to /dev/fb0 come from the very same pixels.
const screenW, screenH = 320, 240

//go:embed fonts/*.bin
var fontFS embed.FS

// bmFont is a fixed-width bitmap font made by tools/fontgen from Terminus (SIL OFL 1.1).
type bmFont struct {
	w, h, ascent int
	rowBytes     int
	runes        []rune
	bits         []byte
}

func loadFont(name string) *bmFont {
	b, err := fontFS.ReadFile("fonts/" + name)
	if err != nil || len(b) < 10 || string(b[:4]) != "MPF1" {
		panic("bad embedded font " + name)
	}
	f := &bmFont{w: int(b[4]), h: int(b[5]), ascent: int(b[6])}
	n := int(binary.LittleEndian.Uint16(b[8:10]))
	f.rowBytes = (f.w + 7) / 8
	p := 10
	for i := 0; i < n; i++ {
		f.runes = append(f.runes, rune(binary.LittleEndian.Uint32(b[p:])))
		p += 4
	}
	f.bits = b[p:]
	if len(f.bits) != n*f.rowBytes*f.h {
		panic("truncated embedded font " + name)
	}
	return f
}

func (f *bmFont) glyph(r rune) []byte {
	i := sort.Search(len(f.runes), func(i int) bool { return f.runes[i] >= r })
	if i == len(f.runes) || f.runes[i] != r {
		switch r {
		case ' ', ' ':
			r = ' '
		default:
			r = '?'
		}
		i = sort.Search(len(f.runes), func(i int) bool { return f.runes[i] >= r })
	}
	sz := f.rowBytes * f.h
	return f.bits[i*sz : (i+1)*sz]
}

// Text sizes for widgets: name -> font and pixel scale. "huge" doubles the 32 px font for big numbers.
type textSize struct {
	f     *bmFont
	scale int
}

var (
	fontS  = loadFont("ter-u12n.bin")
	fontM  = loadFont("ter-u16n.bin")
	fontMB = loadFont("ter-u16b.bin")
	fontL  = loadFont("ter-u24b.bin")
	fontXL = loadFont("ter-u32b.bin")
)

var textSizes = map[string]textSize{
	"s": {fontS, 1}, "m": {fontM, 1}, "mb": {fontMB, 1}, "l": {fontL, 1}, "xl": {fontXL, 1},
	"xxl": {fontL, 2}, "huge": {fontXL, 2},
}

func sizeOf(name, def string) textSize {
	if t, ok := textSizes[name]; ok {
		return t
	}
	return textSizes[def]
}

func (t textSize) height() int { return t.f.h * t.scale }

func (t textSize) width(s string) int { return len([]rune(s)) * t.f.w * t.scale }

// Canvas is a 320x240 frame with clipping to the widget being drawn.
type Canvas struct {
	img  *image.RGBA
	clip image.Rectangle
}

func NewCanvas(bg color.RGBA) *Canvas {
	c := &Canvas{img: image.NewRGBA(image.Rect(0, 0, screenW, screenH))}
	c.clip = c.img.Bounds()
	c.Fill(c.clip, bg)
	return c
}

func (c *Canvas) Clip(r image.Rectangle) { c.clip = r.Intersect(c.img.Bounds()) }

func (c *Canvas) Fill(r image.Rectangle, col color.RGBA) {
	r = r.Intersect(c.clip)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c.img.SetRGBA(x, y, col)
		}
	}
}

func (c *Canvas) set(x, y int, col color.RGBA) {
	if (image.Point{x, y}).In(c.clip) {
		c.img.SetRGBA(x, y, col)
	}
}

// Line draws a 1-pixel line (Bresenham).
func (c *Canvas) Line(x0, y0, x1, y1 int, col color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := sign(x1-x0), sign(y1-y0)
	e := dx + dy
	for {
		c.set(x0, y0, col)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * e
		if e2 >= dy {
			e += dy
			x0 += sx
		}
		if e2 <= dx {
			e += dx
			y0 += sy
		}
	}
}

// Text draws s with its top-left corner at x,y and returns the width.
func (c *Canvas) Text(x, y int, s string, t textSize, col color.RGBA) int {
	f, k := t.f, t.scale
	cx := x
	for _, r := range s {
		g := f.glyph(r)
		for gy := 0; gy < f.h; gy++ {
			row := g[gy*f.rowBytes : (gy+1)*f.rowBytes]
			for gx := 0; gx < f.w; gx++ {
				if row[gx/8]&(0x80>>(gx%8)) == 0 {
					continue
				}
				for dy := 0; dy < k; dy++ {
					for dx := 0; dx < k; dx++ {
						c.set(cx+gx*k+dx, y+gy*k+dy, col)
					}
				}
			}
		}
		cx += f.w * k
	}
	return cx - x
}

// TextIn draws one line of text inside r, aligned left, center or right and vertically centred;
// text that does not fit is cut with an ellipsis.
func (c *Canvas) TextIn(r image.Rectangle, s string, t textSize, align string, col color.RGBA) {
	cw := t.f.w * t.scale
	if fit := r.Dx() / cw; len([]rune(s)) > fit {
		if fit <= 0 {
			return
		}
		s = string([]rune(s)[:fit-1]) + "…"
	}
	w := t.width(s)
	x := r.Min.X
	switch align {
	case "center":
		x = r.Min.X + (r.Dx()-w)/2
	case "right":
		x = r.Max.X - w
	}
	y := r.Min.Y + (r.Dy()-t.height())/2
	c.Text(x, y, s, t, col)
}

func (c *Canvas) PNG() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, c.img)
	return b.Bytes()
}

// RGB666 packs the frame the way the 8800L framebuffer stores it: 3 bytes per pixel, an 18-bit value
// R(17..12) G(11..6) B(5..0) written most significant byte first (white = 03 FF FF).
func (c *Canvas) RGB666() []byte {
	out := make([]byte, 0, screenW*screenH*3)
	for y := 0; y < screenH; y++ {
		for x := 0; x < screenW; x++ {
			p := c.img.RGBAAt(x, y)
			v := uint32(p.R>>2)<<12 | uint32(p.G>>2)<<6 | uint32(p.B>>2)
			out = append(out, byte(v>>16), byte(v>>8), byte(v))
		}
	}
	return out
}

// decodeFramebuffer turns raw /dev/fb0 bytes into an image. 18 bpp in 3 bytes is what the 8800L uses
// (verified); 16 bpp RGB565 and 32 bpp are common elsewhere and decoded the usual way.
func decodeFramebuffer(raw []byte, w, h, stride, bpp int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 || stride <= 0 || len(raw) < stride*h {
		return nil, fmt.Errorf("framebuffer data is too short: %d bytes for %dx%d, stride %d", len(raw), w, h, stride)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	exp6 := func(v uint32) uint8 { return uint8(v<<2 | v>>4) }
	exp5 := func(v uint32) uint8 { return uint8(v<<3 | v>>2) }
	for y := 0; y < h; y++ {
		row := raw[y*stride:]
		for x := 0; x < w; x++ {
			var p color.RGBA
			switch {
			case bpp == 18 && stride >= w*3:
				v := uint32(row[x*3])<<16 | uint32(row[x*3+1])<<8 | uint32(row[x*3+2])
				p = color.RGBA{exp6(v >> 12 & 63), exp6(v >> 6 & 63), exp6(v & 63), 255}
			case bpp == 16:
				v := uint32(binary.LittleEndian.Uint16(row[x*2:]))
				p = color.RGBA{exp5(v >> 11 & 31), uint8((v>>5&63)<<2 | (v>>5&63)>>4), exp5(v & 31), 255}
			case bpp == 32 || bpp == 24:
				n := bpp / 8
				p = color.RGBA{row[x*n+2], row[x*n+1], row[x*n], 255} // BGRA/BGR, the linuxfb default
			default:
				return nil, fmt.Errorf("%d bits per pixel is not supported", bpp)
			}
			img.SetRGBA(x, y, p)
		}
	}
	return img, nil
}

// parseColor reads "#rgb" or "#rrggbb"; anything else gives def.
func parseColor(s string, def color.RGBA) color.RGBA {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return def
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return def
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

var errNoFramebuffer = errors.New("no framebuffer on this device")

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func sign(x int) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
