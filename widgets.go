package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"net/http"
	"sync"
	"time"
)

// WidgetMeta describes a widget type for the designer's palette.
type WidgetMeta struct {
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Desc    string         `json:"desc"`
	Opts    []string       `json:"opts"` // options the designer offers
	Min     [2]int         `json:"min"`  // smallest width and height that still draws sensibly
	Default string         `json:"default"`
	Sizes   []WidgetPreset `json:"sizes"`
}

// WidgetPreset is one S/M/L size: the box and the options that suit it (mostly the text size).
type WidgetPreset struct {
	Key  string            `json:"key"` // S, M, L
	W    int               `json:"w"`
	H    int               `json:"h"`
	Opts map[string]string `json:"opts,omitempty"`
}

func sz(s string) map[string]string { return map[string]string{"size": s} }

// widgetMetas is the single list of widget types; the order is the palette order.
var widgetMetas = []WidgetMeta{
	{Type: "metric", Name: "Big signal value", Desc: "The main signal number with its name and quality.",
		Opts: []string{"metric", "size", "align", "color", "label"}, Min: [2]int{64, 32}, Default: "M",
		Sizes: []WidgetPreset{{"S", 120, 48, sz("l")}, {"M", 200, 80, sz("xxl")}, {"L", 304, 104, sz("huge")}}},
	{Type: "aim", Name: "Antenna aiming", Desc: "Signal, distance from the best position and a bar.",
		Opts: []string{"metric", "color"}, Min: [2]int{200, 120}, Default: "L",
		Sizes: []WidgetPreset{{"S", 240, 160, nil}, {"L", 320, 240, nil}}},
	{Type: "chart", Name: "Signal chart", Desc: "How the signal changed over the last minutes.",
		Opts: []string{"metric", "color"}, Min: [2]int{96, 40}, Default: "M",
		Sizes: []WidgetPreset{{"S", 160, 56, nil}, {"M", 240, 72, nil}, {"L", 304, 112, nil}}},
	{Type: "bars", Name: "Signal bars", Desc: "Five bars, like on a phone.",
		Opts: []string{"metric", "color"}, Min: [2]int{16, 12}, Default: "M",
		Sizes: []WidgetPreset{{"S", 24, 16, nil}, {"M", 40, 28, nil}, {"L", 64, 48, nil}}},
	{Type: "band", Name: "Band and cell", Desc: "Network type, band and frequency; PCI and channel below.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{96, 16}, Default: "M",
		Sizes: []WidgetPreset{{"S", 192, 16, sz("m")}, {"M", 240, 36, sz("m")}, {"L", 304, 56, sz("l")}}},
	{Type: "operator", Name: "Operator", Desc: "The network the router is on.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{64, 16}, Default: "M",
		Sizes: []WidgetPreset{{"S", 120, 16, sz("m")}, {"M", 200, 28, sz("l")}, {"L", 304, 40, sz("xl")}}},
	{Type: "speed", Name: "Download and upload", Desc: "Current internet speed in both directions.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{96, 32}, Default: "M",
		Sizes: []WidgetPreset{{"S", 128, 40, sz("m")}, {"M", 200, 64, sz("l")}, {"L", 304, 80, sz("xl")}}},
	{Type: "traffic", Name: "Traffic total", Desc: "Data received and sent since the connection came up.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{120, 16}, Default: "M",
		Sizes: []WidgetPreset{{"S", 136, 16, sz("s")}, {"M", 200, 20, sz("m")}, {"L", 304, 28, sz("l")}}},
	{Type: "data", Name: "Data session and IP", Desc: "Online, connecting or no internet, with the IP address.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{96, 16}, Default: "M",
		Sizes: []WidgetPreset{{"S", 200, 16, sz("m")}, {"M", 304, 24, sz("mb")}, {"L", 304, 32, sz("l")}}},
	{Type: "devices", Name: "Connected devices", Desc: "How many devices are on the Wi-Fi now.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{72, 16}, Default: "S",
		Sizes: []WidgetPreset{{"S", 96, 16, sz("m")}, {"M", 160, 28, sz("l")}}},
	{Type: "battery", Name: "Battery", Desc: "Charge level and whether it is charging.",
		Opts: []string{"size", "align", "color"}, Min: [2]int{40, 16}, Default: "S",
		Sizes: []WidgetPreset{{"S", 64, 16, sz("m")}, {"M", 112, 20, sz("mb")}, {"L", 160, 32, sz("l")}}},
	{Type: "clock", Name: "Clock", Desc: "Time of day in your time zone.",
		Opts: []string{"size", "align", "color", "offset"}, Min: [2]int{48, 16}, Default: "M",
		Sizes: []WidgetPreset{{"S", 48, 16, sz("m")}, {"M", 80, 28, sz("l")}, {"L", 160, 64, sz("huge")}}},
	{Type: "text", Name: "Free text", Desc: "Any text you type, such as a name or a note.",
		Opts: []string{"text", "size", "align", "color"}, Min: [2]int{24, 12}, Default: "M",
		Sizes: []WidgetPreset{{"S", 80, 16, sz("m")}, {"M", 160, 28, sz("l")}, {"L", 304, 40, sz("xl")}}},
}

// widgetTypes maps a type to its options; derived from widgetMetas.
var widgetTypes = func() map[string][]string {
	m := map[string][]string{}
	for _, w := range widgetMetas {
		m[w.Type] = w.Opts
	}
	return m
}()

func widgetMeta(typ string) (WidgetMeta, bool) {
	for _, w := range widgetMetas {
		if w.Type == typ {
			return w, true
		}
	}
	return WidgetMeta{}, false
}

func (m WidgetMeta) preset(key string) WidgetPreset {
	for _, p := range m.Sizes {
		if p.Key == key {
			return p
		}
	}
	for _, p := range m.Sizes {
		if p.Key == m.Default {
			return p
		}
	}
	return m.Sizes[0]
}

// sampleScreenData is fixed, made-up data for palette thumbnails: they must not change from one
// request to the next, so they can be cached.
func sampleScreenData() ScreenData {
	st := Status{
		TS: time.Date(2026, 1, 1, 12, 34, 0, 0, time.UTC).UnixMilli(),
		Network: Network{Operator: "Vodafone UA", Tech: "LTE", TechName: "LTE", Band: "B3", EARFCN: ip(1700),
			PCI: ip(80), FreqMHz: fp(1855), CellID: "143415071", TAC: "3221", Reg: "home"},
		Signal:  Signal{RSRP: fp(-82), RSRQ: fp(-9), SINR: fp(17)},
		WAN:     WAN{Up: true, IP: "10.58.8.156", State: "connected", RxRate: 1.55e6, TxRate: 1.6e5, RxBytes: 3_412_000_000, TxBytes: 412_000_000},
		Battery: Battery{Present: true, Percent: ip(78), Status: "charging"},
		Clients: []Client{{Online: true}, {Online: true}},
	}
	var h History
	for i := 0; i < 120; i++ {
		s := st
		s.TS = st.TS - int64(120-i)*2000
		v := -86 + 5*math.Sin(float64(i)/9) + 2*math.Sin(float64(i)/2.3)
		s.Signal = Signal{RSRP: fp(math.Round(v)), SINR: fp(math.Round(14 + v/10)), RSRQ: fp(-9)}
		h.add(&s, 2*time.Second)
	}
	return ScreenData{St: st, Hist: h, AimBest: map[string]float64{"rsrp": -76}}
}

// widgetPreview renders one widget alone, cropped to its box, at 1x.
func widgetPreview(typ, size string) ([]byte, bool) {
	m, ok := widgetMeta(typ)
	if !ok {
		return nil, false
	}
	p := m.preset(size)
	opts := map[string]string{}
	for k, v := range p.Opts {
		opts[k] = v
	}
	if typ == "text" {
		opts["text"] = "Hello"
	}
	l := Layout{Pages: []Page{{BG: "#ffffff", Widgets: []Widget{{Type: typ, W: p.W, H: p.H, Opts: opts}}}}}
	c := RenderPage(l, 0, sampleScreenData())
	var b bytes.Buffer
	_ = png.Encode(&b, c.img.SubImage(image.Rect(0, 0, p.W, p.H)))
	return b.Bytes(), true
}

// previewCache keeps thumbnails: they depend only on the type and size, so each is drawn once.
var previewCache = struct {
	sync.Mutex
	m map[string][]byte
}{m: map[string][]byte{}}

func cachedWidgetPreview(typ, size string) ([]byte, bool) {
	key := typ + "/" + size
	previewCache.Lock()
	b, ok := previewCache.m[key]
	previewCache.Unlock()
	if ok {
		return b, true
	}
	b, ok = widgetPreview(typ, size)
	if ok {
		previewCache.Lock()
		previewCache.m[key] = b
		previewCache.Unlock()
	}
	return b, ok
}

func registerWidgetAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/screen/widgets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"widgets": widgetMetas, "screen": [2]int{screenW, screenH}})
	})
	mux.HandleFunc("GET /api/screen/widget-preview", func(w http.ResponseWriter, r *http.Request) {
		b, ok := cachedWidgetPreview(r.URL.Query().Get("type"), r.URL.Query().Get("size"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown widget"})
			return
		}
		w.Header().Set("Content-Type", "image/png")
		// The URL carries the panel version, so a new version gets new thumbnails.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(b)
	})
}
