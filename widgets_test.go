package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// Every widget's metadata is consistent and every size preset renders a non-empty thumbnail of its size.
func TestWidgetMetasAndPreviews(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range widgetMetas {
		if seen[m.Type] {
			t.Errorf("%s listed twice", m.Type)
		}
		seen[m.Type] = true
		if m.Name == "" || m.Desc == "" || len(m.Sizes) == 0 {
			t.Errorf("%s: name, description and sizes are required", m.Type)
		}
		if m.preset(m.Default).Key != m.Default {
			t.Errorf("%s: default size %q is not a preset", m.Type, m.Default)
		}
		for _, p := range m.Sizes {
			if p.W < m.Min[0] || p.H < m.Min[1] || p.W > screenW || p.H > screenH {
				t.Errorf("%s %s: %dx%d is outside %v..320x240", m.Type, p.Key, p.W, p.H, m.Min)
			}
			b, ok := cachedWidgetPreview(m.Type, p.Key)
			if !ok {
				t.Fatalf("%s %s: no preview", m.Type, p.Key)
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil || img.Bounds() != image.Rect(0, 0, p.W, p.H) {
				t.Fatalf("%s %s: %v %v", m.Type, p.Key, err, img.Bounds())
			}
			rgba := image.NewRGBA(img.Bounds())
			for y := 0; y < p.H; y++ {
				for x := 0; x < p.W; x++ {
					rgba.Set(x, y, img.At(x, y))
				}
			}
			if !drewSomething(rgba) {
				t.Errorf("%s %s: thumbnail is empty", m.Type, p.Key)
			}
			if b2, _ := cachedWidgetPreview(m.Type, p.Key); !bytes.Equal(b, b2) {
				t.Errorf("%s %s: cache returned different bytes", m.Type, p.Key)
			}
		}
	}
	if _, ok := cachedWidgetPreview("nope", "M"); ok {
		t.Error("unknown type rendered")
	}
}

func TestValidateMinSize(t *testing.T) {
	l := Layout{Pages: []Page{{Widgets: []Widget{{Type: "aim", W: 50, H: 20}}}}}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if w := l.Pages[0].Widgets[0]; w.W != 200 || w.H != 120 {
		t.Fatalf("aim must grow to its minimum 200x120, got %dx%d", w.W, w.H)
	}
}
