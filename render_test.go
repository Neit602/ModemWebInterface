package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// Real pixels read from the 8800L's /dev/fb0 (stock home screen): white, black and the light grey
// of a disabled label (60 of 63 in every channel).
var fbPixels = []struct {
	raw  [3]byte
	want color.RGBA
}{
	{[3]byte{0x03, 0xFF, 0xFF}, color.RGBA{255, 255, 255, 255}},
	{[3]byte{0x00, 0x00, 0x00}, color.RGBA{0, 0, 0, 255}},
	{[3]byte{3, 207, 60}, color.RGBA{243, 243, 243, 255}},
}

func TestDecodeFramebufferRGB666(t *testing.T) {
	raw := make([]byte, 0, 9)
	for _, p := range fbPixels {
		raw = append(raw, p.raw[:]...)
	}
	img, err := decodeFramebuffer(raw, 3, 1, 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range fbPixels {
		if got := img.RGBAAt(i, 0); got != p.want {
			t.Errorf("pixel %d: got %v, want %v", i, got, p.want)
		}
	}
	if _, err := decodeFramebuffer(raw[:5], 3, 1, 9, 18); err == nil {
		t.Error("short data must be an error")
	}
}

func TestRGB666RoundTrip(t *testing.T) {
	c := NewCanvas(colWhite)
	c.Fill(image.Rect(0, 0, 10, 10), color.RGBA{243, 243, 243, 255})
	fb := c.RGB666()
	if len(fb) != screenW*screenH*3 {
		t.Fatalf("frame is %d bytes", len(fb))
	}
	if !bytes.Equal(fb[:3], []byte{3, 207, 60}) || !bytes.Equal(fb[len(fb)-3:], []byte{0x03, 0xFF, 0xFF}) {
		t.Fatalf("encoded %v ... %v", fb[:3], fb[len(fb)-3:])
	}
	img, _ := decodeFramebuffer(fb, screenW, screenH, screenW*3, 18)
	if img.RGBAAt(5, 5) != c.img.RGBAAt(5, 5) || img.RGBAAt(300, 200) != colWhite {
		t.Fatal("decode(encode(frame)) differs")
	}
}

func TestFontsHaveCyrillic(t *testing.T) {
	for name, ts := range textSizes {
		q := ts.f.glyph('?')
		for _, r := range "АБВҐЄІЇЖЯабвґєіїжя№−·↓↑…≈" {
			if bytes.Equal(ts.f.glyph(r), q) {
				t.Errorf("font %s has no glyph for %q", name, r)
			}
		}
	}
	if w := textSizes["huge"].width("−97"); w != 3*16*2 {
		t.Errorf("huge width %d", w)
	}
}

func TestTextEllipsis(t *testing.T) {
	c := NewCanvas(colWhite)
	c.TextIn(image.Rect(0, 0, 40, 16), "Vodafone UA", textSizes["m"], "left", colBlack) // 5 cells of 8 px
	if c.img.RGBAAt(45, 8) != colWhite {
		t.Fatal("text drawn outside its box")
	}
}

func TestLayoutValidate(t *testing.T) {
	l := Layout{Pages: []Page{{Widgets: []Widget{{Type: "metric", X: 300, Y: -5, W: 100, H: 500}}}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	w := l.Pages[0].Widgets[0]
	if w.W != 100 || w.H != screenH || w.X != screenW-100 || w.Y != 0 {
		t.Fatalf("not clamped: %+v", w)
	}
	bad := Layout{Pages: []Page{{Widgets: []Widget{{Type: "nope"}}}}}
	if bad.Validate() == nil {
		t.Fatal("unknown widget accepted")
	}
	if (&Layout{}).Validate() == nil {
		t.Fatal("empty layout accepted")
	}
}

// Every widget type, at every size, on LTE and 3G data, renders without panicking and draws something.
func TestRenderAllWidgets(t *testing.T) {
	lte, _, _ := parseModem2State(modem2StateLTE)
	_, g3sig, _ := parseModem2State(modem2State3G)
	g3net := Network{Tech: "3G", TechName: "HSPA+", Operator: "Vodafone UA", CellID: "86690833", TAC: "3221", Reg: "home"}
	sts := []Status{
		{TS: time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC).UnixMilli(), Network: lte, Signal: Signal{RSRP: fp(-69), RSRQ: fp(-8), SINR: fp(17)},
			WAN: WAN{Up: true, IP: "10.58.8.156", RxRate: 2.5e6, TxRate: 1.2e5}, Battery: Battery{Percent: ip(78), Status: "charging"}},
		{TS: 1000, Network: g3net, Signal: g3sig},
	}
	for _, st := range sts {
		var h History
		for i := 0; i < 50; i++ {
			st.TS += 2000
			h.add(&st, 2*time.Second)
		}
		d := ScreenData{St: st, Hist: h, AimBest: map[string]float64{"rsrp": -60}}
		for typ := range widgetTypes {
			for size := range textSizes {
				l := Layout{Pages: []Page{{Widgets: []Widget{{Type: typ, X: 0, Y: 0, W: 320, H: 120, Opts: map[string]string{"size": size}}}}}}
				c := RenderPage(l, 0, d)
				if typ != "bars" && !drewSomething(c.img) {
					t.Errorf("%s/%s on %s drew nothing", typ, size, st.Network.Tech)
				}
			}
		}
	}
	// The default layout renders and encodes as a PNG of the screen size.
	b := RenderPage(DefaultLayout(), 0, ScreenData{St: sts[0]}).PNG()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds() != image.Rect(0, 0, screenW, screenH) {
		t.Fatalf("png %v %v", err, img.Bounds())
	}
	if js, err := json.Marshal(DefaultLayout()); err != nil || len(js) == 0 {
		t.Fatal(err)
	}
}

func drewSomething(img *image.RGBA) bool {
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 255 || img.Pix[i+1] != 255 || img.Pix[i+2] != 255 {
			return true
		}
	}
	return false
}

func TestParseOffset(t *testing.T) {
	for in, want := range map[string]time.Duration{"+2": 2 * time.Hour, "-3": -3 * time.Hour, "+5:30": 5*time.Hour + 30*time.Minute, "": 0, "x": 0, "+99": 0} {
		if got := parseOffset(in); got != want {
			t.Errorf("parseOffset(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTestFrame(t *testing.T) {
	c := TestFrame("5 s")
	fb := c.RGB666()
	// The first bar is pure red: R=63, G=0, B=0 -> 0x3F000 -> bytes 03 F0 00.
	i := (40*screenW + 20) * 3 // inside the red bar, away from the border
	if fb[i] != 0x03 || fb[i+1] != 0xF0 || fb[i+2] != 0x00 {
		t.Fatalf("red pixel encoded as % x", fb[i:i+3])
	}
}
