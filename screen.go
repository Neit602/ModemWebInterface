package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A Layout describes what to draw on the 320x240 device screen: pages of widgets placed in pixels.
// The web designer edits it as JSON; the renderer draws it for the preview and for the screen itself.
type Layout struct {
	Version int    `json:"version"`
	Pages   []Page `json:"pages"`
}

type Page struct {
	Name    string   `json:"name"`
	BG      string   `json:"bg"`
	Widgets []Widget `json:"widgets"`
}

type Widget struct {
	Type string            `json:"type"`
	X    int               `json:"x"`
	Y    int               `json:"y"`
	W    int               `json:"w"`
	H    int               `json:"h"`
	Opts map[string]string `json:"opts,omitempty"`
}

const (
	maxPages   = 8
	maxWidgets = 40
)

// Validate clamps a layout to the screen and rejects what the renderer cannot draw.
func (l *Layout) Validate() error {
	if len(l.Pages) == 0 {
		return errors.New("the layout has no pages")
	}
	if len(l.Pages) > maxPages {
		return fmt.Errorf("at most %d pages", maxPages)
	}
	l.Version = 1
	for pi := range l.Pages {
		p := &l.Pages[pi]
		if len(p.Widgets) > maxWidgets {
			return fmt.Errorf("page %d: at most %d widgets", pi+1, maxWidgets)
		}
		if len([]rune(p.Name)) > 30 {
			p.Name = string([]rune(p.Name)[:30])
		}
		for wi := range p.Widgets {
			w := &p.Widgets[wi]
			meta, ok := widgetMeta(w.Type)
			if !ok {
				return fmt.Errorf("page %d: unknown widget %q", pi+1, w.Type)
			}
			w.W = clamp(w.W, meta.Min[0], screenW)
			w.H = clamp(w.H, meta.Min[1], screenH)
			w.X = clamp(w.X, 0, screenW-w.W)
			w.Y = clamp(w.Y, 0, screenH-w.H)
			for k, v := range w.Opts {
				if len(v) > 200 {
					w.Opts[k] = v[:200]
				}
			}
		}
	}
	return nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// DefaultLayout: a signal page, an antenna aiming page and an internet page.
func DefaultLayout() Layout {
	return Layout{Version: 1, Pages: []Page{
		{Name: "Signal", BG: "#ffffff", Widgets: []Widget{
			{Type: "operator", X: 8, Y: 4, W: 184, H: 24, Opts: map[string]string{"size": "mb"}},
			{Type: "battery", X: 232, Y: 4, W: 80, H: 24, Opts: map[string]string{"size": "mb", "align": "right"}},
			{Type: "band", X: 8, Y: 30, W: 304, H: 20, Opts: map[string]string{"size": "m"}},
			{Type: "metric", X: 8, Y: 56, W: 304, H: 96, Opts: map[string]string{"metric": "auto", "size": "huge"}},
			{Type: "chart", X: 8, Y: 160, W: 304, H: 72, Opts: map[string]string{"metric": "auto"}},
		}},
		{Name: "Aiming", BG: "#ffffff", Widgets: []Widget{
			{Type: "aim", X: 0, Y: 0, W: 320, H: 240, Opts: map[string]string{"metric": "auto"}},
		}},
		{Name: "Internet", BG: "#ffffff", Widgets: []Widget{
			{Type: "data", X: 8, Y: 8, W: 304, H: 24, Opts: map[string]string{"size": "mb"}},
			{Type: "speed", X: 8, Y: 40, W: 304, H: 80, Opts: map[string]string{"size": "l"}},
			{Type: "traffic", X: 8, Y: 128, W: 304, H: 24, Opts: map[string]string{"size": "m"}},
			{Type: "devices", X: 8, Y: 160, W: 200, H: 24, Opts: map[string]string{"size": "m"}},
			{Type: "clock", X: 208, Y: 200, W: 104, H: 32, Opts: map[string]string{"size": "l", "align": "right", "offset": "+2"}},
		}},
	}}
}

// ScreenData is everything widgets draw from.
type ScreenData struct {
	St      Status
	Hist    History
	AimBest map[string]float64 // best value per metric since aiming started
}

// Signal metrics, the same scales and thresholds as the web UI.
type metricDef struct {
	name, unit string
	min, max   float64
	digits     int
	steps      []float64
}

var metricDefs = map[string]metricDef{
	"rsrp": {"RSRP", "dBm", -125, -65, 0, []float64{-80, -90, -100, -110}},
	"sinr": {"SINR", "dB", -5, 30, 0, []float64{20, 13, 0}},
	"rsrq": {"RSRQ", "dB", -20, -3, 1, []float64{-10, -13, -16, -19}},
	"rscp": {"RSCP", "dBm", -120, -50, 0, []float64{-85, -95, -105}},
	"ecio": {"Ec/Io", "dB", -24, 0, 0, []float64{-6, -10, -14}},
}

var (
	colBlack    = color.RGBA{0, 0, 0, 255}
	colWhite    = color.RGBA{255, 255, 255, 255}
	colMuted    = color.RGBA{0x74, 0x76, 0x76, 255}
	colGrid     = color.RGBA{0xd8, 0xdc, 0xd6, 255}
	colAccent   = color.RGBA{0x1e, 0x55, 0xb3, 255}
	levelNames  = []string{"Excellent", "Good", "Fair", "Poor", "Very poor"}
	levelColors = []color.RGBA{{0x0c, 0xa3, 0x0c, 255}, {0x0c, 0xa3, 0x0c, 255}, {0xe0, 0x9a, 0x00, 255}, {0xec, 0x83, 0x5a, 255}, {0xd0, 0x3b, 0x3b, 255}}
)

// resolveMetric turns "auto" into the main metric of the current network type.
func resolveMetric(m string, st Status) string {
	if _, ok := metricDefs[m]; ok {
		return m
	}
	if st.Network.Tech == "3G" {
		return "rscp"
	}
	return "rsrp"
}

func metricValue(m string, s Signal) *float64 {
	switch m {
	case "rsrp":
		return s.RSRP
	case "sinr":
		return s.SINR
	case "rsrq":
		return s.RSRQ
	case "rscp":
		return s.RSCP
	case "ecio":
		return s.EcIo
	}
	return nil
}

func metricHistory(m string, h History) []*float64 {
	switch m {
	case "rsrp":
		return h.RSRP
	case "sinr":
		return h.SINR
	case "rsrq":
		return h.RSRQ
	case "rscp":
		return h.RSCP
	case "ecio":
		return h.EcIo
	}
	return nil
}

// level returns 0 (excellent) .. 4 (very poor), or -1 without a value.
func (d metricDef) level(v *float64) int {
	if v == nil {
		return -1
	}
	for i, s := range d.steps {
		if *v >= s {
			return i
		}
	}
	return min(len(d.steps), len(levelNames)-1)
}

func (d metricDef) format(v *float64) string {
	if v == nil {
		return "—"
	}
	return strings.Replace(strconv.FormatFloat(*v, 'f', d.digits, 64), "-", "−", 1)
}

func (d metricDef) frac(v float64) float64 {
	return math.Max(0, math.Min(1, (v-d.min)/(d.max-d.min)))
}

// RenderPage draws one page of the layout.
func RenderPage(l Layout, page int, d ScreenData) *Canvas {
	if page < 0 || page >= len(l.Pages) {
		page = 0
	}
	p := l.Pages[page]
	c := NewCanvas(parseColor(p.BG, colWhite))
	for _, w := range p.Widgets {
		r := image.Rect(w.X, w.Y, w.X+w.W, w.Y+w.H)
		c.Clip(r)
		drawWidget(c, w, r, d)
	}
	return c
}

func opt(w Widget, k, def string) string {
	if v := w.Opts[k]; v != "" {
		return v
	}
	return def
}

func drawWidget(c *Canvas, w Widget, r image.Rectangle, d ScreenData) {
	st := d.St
	fg := parseColor(opt(w, "color", ""), colBlack)
	ts := sizeOf(opt(w, "size", ""), "m")
	align := opt(w, "align", "left")
	line := func(s string) { c.TextIn(r, s, ts, align, fg) }

	switch w.Type {
	case "text":
		line(opt(w, "text", "Text"))
	case "operator":
		s := st.Network.Operator
		if s == "" {
			s = "No network"
		}
		line(s)
	case "clock":
		// The router keeps UTC; "offset" is the local time zone, e.g. "+2" or "+5:30", or "auto" for the
		// time zone the network reports.
		off := parseOffset(opt(w, "offset", ""))
		if opt(w, "offset", "") == "auto" && st.WAN.TZMin != nil {
			off = time.Duration(*st.WAN.TZMin) * time.Minute
		}
		t := time.UnixMilli(st.TS).UTC().Add(off)
		if t.Year() < 2020 { // the router clock starts at 1970 until the network sets it
			line("--:--")
		} else {
			line(t.Format("15:04"))
		}
	case "battery":
		b := st.Battery
		s := "—"
		if b.Percent != nil {
			s = strconv.Itoa(*b.Percent) + "%"
			if b.Status == "charging" || (b.ExtPower != nil && *b.ExtPower) {
				if ts.width(s+" charging") <= r.Dx() {
					s += " charging"
				} else {
					s += "+"
				}
			}
		}
		line(s)
	case "devices":
		n := 0
		for _, cl := range st.Clients {
			if cl.Online {
				n++
			}
		}
		switch n {
		case 0:
			line("No devices")
		case 1:
			line("1 device")
		default:
			line(strconv.Itoa(n) + " devices")
		}
	case "data":
		s := "No internet"
		switch {
		case st.WAN.Up && st.WAN.IP != "":
			s = "Online · " + st.WAN.IP
		case st.WAN.State == "connecting":
			s = "Connecting…"
		}
		line(s)
	case "traffic":
		line("↓ " + bytesText(st.WAN.RxBytes) + "   ↑ " + bytesText(st.WAN.TxBytes))
	case "speed":
		drawLines(c, r, ts, align, fg, []string{"↓ " + rateText(st.WAN.RxRate), "↑ " + rateText(st.WAN.TxRate)})
	case "band":
		n := st.Network
		var a, b string
		switch {
		case n.Tech == "LTE":
			a = "LTE"
			if n.Band != "" {
				a += " " + n.Band
			}
			if n.FreqMHz != nil {
				a += " · " + strconv.FormatFloat(*n.FreqMHz, 'f', 1, 64) + " MHz"
			}
			if n.PCI != nil {
				b = "PCI " + strconv.Itoa(*n.PCI)
			}
			if n.EARFCN != nil {
				b += " · EARFCN " + strconv.Itoa(*n.EARFCN)
			}
		case n.Tech != "":
			a = n.Tech
			if n.TechName != "" && n.TechName != n.Tech {
				a += " " + n.TechName
			}
			if n.TAC != "" {
				b = "LAC " + n.TAC
			}
			if n.CellID != "" {
				b += " · cell " + n.CellID
			}
		default:
			a = "No network"
		}
		if b != "" && r.Dy() >= 2*ts.height() {
			drawLines(c, r, ts, align, fg, []string{a, strings.TrimPrefix(b, " · ")})
		} else {
			line(a)
		}
	case "temp":
		v, name := st.SoCTempC, "Chip"
		if opt(w, "sensor", "soc") == "battery" {
			v, name = st.Battery.TempC, "Battery"
		}
		s := "—"
		if v != nil {
			s = strconv.FormatFloat(*v, 'f', 0, 64) + " °C"
			if ts.width(name+" "+s) <= r.Dx() {
				s = name + " " + s
			}
		}
		line(s)
	case "power":
		line(fitText(ts, r.Dx(), powerTexts(st.Battery)...))
	case "sms":
		switch {
		case st.SMSUnread == nil:
			line("SMS —")
		case *st.SMSUnread == 0:
			line("No new SMS")
		default:
			line(fitText(ts, r.Dx(), strconv.Itoa(*st.SMSUnread)+" unread SMS", strconv.Itoa(*st.SMSUnread)+" SMS"))
		}
	case "lifetime":
		if l := st.Lifetime; l != nil {
			t := "↓ " + bytesText(uint64(l.Down)) + "  ↑ " + bytesText(uint64(l.Up))
			line(fitText(ts, r.Dx(), "Since first use "+t, "All time "+t, t))
		} else {
			line("—")
		}
	case "weakest":
		var weak *Client
		for i, cl := range st.Clients {
			if cl.Online && cl.Signal != nil && (weak == nil || *cl.Signal < *weak.Signal) {
				weak = &st.Clients[i]
			}
		}
		if weak == nil {
			line("No Wi-Fi devices")
			break
		}
		name := weak.Name
		if name == "" {
			name = "Unnamed"
		}
		sig := strconv.Itoa(*weak.Signal) + " dBm"
		line(fitText(ts, r.Dx(), "Weakest: "+name+" "+sig, name+" "+sig, sig))
	case "metric":
		drawMetric(c, w, r, d, fg, ts, align)
	case "bars":
		m := resolveMetric(opt(w, "metric", "auto"), st)
		drawBars(c, r, metricDefs[m].level(metricValue(m, st.Signal)), fg)
	case "chart":
		drawChart(c, w, r, d, fg)
	case "aim":
		drawAim(c, w, r, d, fg)
	}
}

// parseOffset reads "+2", "-3", "+5:30" or "2" as a time zone offset; anything else is UTC.
func parseOffset(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	hm := strings.SplitN(s, ":", 2)
	h, err := strconv.Atoi(hm[0])
	if err != nil || h > 14 {
		return 0
	}
	m := 0
	if len(hm) == 2 {
		if m, err = strconv.Atoi(hm[1]); err != nil || m > 59 {
			return 0
		}
	}
	d := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	if neg {
		d = -d
	}
	return d
}

// drawLines stacks lines of text, vertically centred as a block.
func drawLines(c *Canvas, r image.Rectangle, ts textSize, align string, fg color.RGBA, lines []string) {
	lh := ts.height() + 2
	y := r.Min.Y + (r.Dy()-lh*len(lines))/2
	for _, s := range lines {
		c.TextIn(image.Rect(r.Min.X, y, r.Max.X, y+lh), s, ts, align, fg)
		y += lh
	}
}

func drawMetric(c *Canvas, w Widget, r image.Rectangle, d ScreenData, fg color.RGBA, ts textSize, align string) {
	m := resolveMetric(opt(w, "metric", "auto"), d.St)
	def := metricDefs[m]
	v := metricValue(m, d.St.Signal)
	small := textSizes["mb"]
	top := r.Min.Y
	if opt(w, "label", "on") != "off" {
		lv := def.level(v)
		label := def.name
		lc := colMuted
		if lv >= 0 {
			label += " · " + levelNames[lv]
			lc = levelColors[lv]
		}
		c.TextIn(image.Rect(r.Min.X, top, r.Max.X, top+small.height()), label, small, align, lc)
		top += small.height() + 2
	}
	val, unit := def.format(v), " "+def.unit
	for _, cand := range []string{opt(w, "size", "huge"), "xxl", "xl", "l", "m"} {
		ts = sizeOf(cand, "huge")
		if ts.width(val)+small.width(unit) <= r.Dx() && ts.height() <= r.Max.Y-top {
			break
		}
	}
	wv := ts.width(val) + small.width(unit)
	x := r.Min.X
	switch align {
	case "center":
		x += (r.Dx() - wv) / 2
	case "right":
		x = r.Max.X - wv
	}
	y := top + (r.Max.Y-top-ts.height())/2
	x += c.Text(x, y, val, ts, fg)
	c.Text(x, y+ts.height()-small.height()-ts.scale*2, unit, small, colMuted)
}

func drawBars(c *Canvas, r image.Rectangle, lv int, fg color.RGBA) {
	lit := 0
	if lv >= 0 {
		lit = 5 - lv
	}
	gap := max(2, r.Dx()/20)
	bw := (r.Dx() - 4*gap) / 5
	for i := 0; i < 5; i++ {
		h := r.Dy() * (i + 1) / 5
		x := r.Min.X + i*(bw+gap)
		col := colGrid
		if i < lit {
			col = fg
		}
		c.Fill(image.Rect(x, r.Max.Y-h, x+bw, r.Max.Y), col)
	}
}

func drawChart(c *Canvas, w Widget, r image.Rectangle, d ScreenData, fg color.RGBA) {
	m := resolveMetric(opt(w, "metric", "auto"), d.St)
	def := metricDefs[m]
	vals := metricHistory(m, d.Hist)
	small := textSizes["s"]
	c.TextIn(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+small.height()), def.name+", last "+histSpan(d.Hist), small, "left", colMuted)
	plot := image.Rect(r.Min.X, r.Min.Y+small.height()+2, r.Max.X, r.Max.Y)
	for _, s := range def.steps {
		y := plot.Max.Y - 1 - int(def.frac(s)*float64(plot.Dy()-1))
		c.Fill(image.Rect(plot.Min.X, y, plot.Max.X, y+1), colGrid)
	}
	n := len(vals)
	if n > plot.Dx() { // one sample per pixel column at most
		vals = vals[n-plot.Dx():]
		n = len(vals)
	}
	px, py, have := 0, 0, false
	for i, v := range vals {
		if v == nil {
			have = false
			continue
		}
		x := plot.Min.X + i*(plot.Dx()-1)/max(1, n-1)
		y := plot.Max.Y - 1 - int(def.frac(*v)*float64(plot.Dy()-1))
		if have {
			c.Line(px, py, x, y, fg)
			c.Line(px, py+1, x, y+1, fg)
		}
		px, py, have = x, y, true
	}
}

func drawAim(c *Canvas, w Widget, r image.Rectangle, d ScreenData, fg color.RGBA) {
	m := resolveMetric(opt(w, "metric", "auto"), d.St)
	def := metricDefs[m]
	v := metricValue(m, d.St.Signal)
	best, hasBest := d.AimBest[m]
	if v != nil && (!hasBest || *v > best) {
		best, hasBest = *v, true
	}
	mb, l := textSizes["mb"], textSizes["l"]
	pad := 8
	in := r.Inset(pad)
	lv := def.level(v)
	head := def.name + " · antenna aiming"
	c.TextIn(image.Rect(in.Min.X, in.Min.Y, in.Max.X, in.Min.Y+mb.height()), head, mb, "left", colMuted)

	barH := 14
	barTop := in.Max.Y - barH
	deltaTop := barTop - 6 - l.height()
	valRect := image.Rect(in.Min.X, in.Min.Y+mb.height()+4, in.Max.X, deltaTop-4)
	val := def.format(v)
	ts := textSizes["huge"]
	for _, cand := range []string{"huge", "xxl", "xl"} {
		ts = textSizes[cand]
		if ts.width(val)+mb.width(" "+def.unit) <= valRect.Dx() && ts.height() <= valRect.Dy() {
			break
		}
	}
	vc := fg
	if lv >= 0 {
		vc = levelColors[lv]
	}
	x := valRect.Min.X + (valRect.Dx()-ts.width(val)-mb.width(" "+def.unit))/2
	y := valRect.Min.Y + (valRect.Dy()-ts.height())/2
	x += c.Text(x, y, val, ts, vc)
	c.Text(x, y+ts.height()-mb.height()-ts.scale*2, " "+def.unit, mb, colMuted)

	delta := "No signal"
	if v != nil {
		if *v >= best {
			delta = "Best position so far"
		} else {
			bd := metricDef{digits: def.digits}
			delta = bd.format(fp(best-*v)) + " " + def.unit + " below the best"
		}
	}
	dt := l // a smaller font when the line does not fit the width
	for _, cand := range []string{"l", "mb", "m", "s"} {
		if dt = textSizes[cand]; dt.width(delta) <= in.Dx() {
			break
		}
	}
	c.TextIn(image.Rect(in.Min.X, deltaTop, in.Max.X, deltaTop+l.height()), delta, dt, "center", fg)

	bar := image.Rect(in.Min.X, barTop, in.Max.X, in.Max.Y)
	c.Fill(bar, colGrid)
	if v != nil {
		c.Fill(image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+int(def.frac(*v)*float64(bar.Dx())), bar.Max.Y), vc)
	}
	if hasBest {
		bx := bar.Min.X + int(def.frac(best)*float64(bar.Dx()-2))
		c.Fill(image.Rect(bx, bar.Min.Y-4, bx+2, bar.Max.Y+4), colAccent)
	}
}

func histSpan(h History) string {
	if len(h.T) < 2 {
		return "few seconds"
	}
	s := (h.T[len(h.T)-1] - h.T[0]) / 1000
	if s < 60 {
		return strconv.FormatInt(s, 10) + " s"
	}
	return strconv.FormatInt((s+30)/60, 10) + " min"
}

func rateText(bps float64) string {
	b := bps * 8
	switch {
	case b >= 1e6:
		return strconv.FormatFloat(b/1e6, 'f', 1, 64) + " Mbps"
	case b >= 1e3:
		return strconv.FormatFloat(b/1e3, 'f', 0, 64) + " kbps"
	}
	return strconv.FormatFloat(b, 'f', 0, 64) + " bps"
}

// fitText returns the first text that fits the width, or the last one.
func fitText(ts textSize, width int, texts ...string) string {
	for _, t := range texts {
		if ts.width(t) <= width {
			return t
		}
	}
	return texts[len(texts)-1]
}

// powerTexts describes the battery power from the longest to the shortest wording.
func powerTexts(b Battery) []string {
	if b.PowerW == nil {
		if b.Percent != nil {
			return []string{"Battery " + strconv.Itoa(*b.Percent) + "%"}
		}
		return []string{"—"}
	}
	w := strconv.FormatFloat(math.Abs(*b.PowerW), 'f', 1, 64) + " W"
	switch {
	case *b.PowerW < -0.05:
		if b.TimeLeftS != nil {
			left := hoursText(*b.TimeLeftS)
			return []string{"−" + w + " · " + left + " left", "−" + w + " " + left, left + " left"}
		}
		return []string{"−" + w + " on battery", "−" + w}
	case *b.PowerW > 0.05:
		return []string{"+" + w + " charging", "+" + w}
	}
	if b.Phase == "full" {
		return []string{"Full, on external power", "Full"}
	}
	return []string{w}
}

func hoursText(s int64) string {
	h, m := s/3600, s%3600/60
	if h > 0 {
		return strconv.FormatInt(h, 10) + " h " + strconv.FormatInt(m, 10) + " min"
	}
	return strconv.FormatInt(m, 10) + " min"
}

func bytesText(n uint64) string {
	f := float64(n)
	switch {
	case f >= 1e9:
		return strconv.FormatFloat(f/1e9, 'f', 1, 64) + " GB"
	case f >= 1e6:
		return strconv.FormatFloat(f/1e6, 'f', 0, 64) + " MB"
	}
	return strconv.FormatFloat(f/1e3, 'f', 0, 64) + " KB"
}

// LayoutStore keeps the current layout in memory and, when -layout-dir is set, in a file there.
type LayoutStore struct {
	mu   sync.Mutex
	dir  string
	cur  Layout
	from string // "default", "file", "memory"
}

const layoutFile = "screen-layout.json"

func NewLayoutStore(dir string) *LayoutStore {
	s := &LayoutStore{dir: dir, cur: DefaultLayout(), from: "default"}
	if dir == "" {
		return s
	}
	if b, err := os.ReadFile(filepath.Join(dir, layoutFile)); err == nil {
		var l Layout
		if json.Unmarshal(b, &l) == nil && l.Validate() == nil {
			s.cur, s.from = l, "file"
		}
	}
	return s
}

func (s *LayoutStore) Get() (Layout, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.from, s.dir != ""
}

// Set replaces the layout; it is written to disk only when a layout directory was configured.
func (s *LayoutStore) Set(l Layout) (saved bool, err error) {
	if err := l.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur, s.from = l, "memory"
	if s.dir == "" {
		return false, nil
	}
	b, _ := json.MarshalIndent(l, "", " ")
	tmp := filepath.Join(s.dir, layoutFile+".tmp")
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, layoutFile)); err != nil {
		return false, err
	}
	s.from = "file"
	return true, nil
}

// StockLikeLayout imitates the stock home screen for the mirror in demo mode.
func StockLikeLayout() Layout {
	t := func(x, y, w, h int, text, size, align string) Widget {
		return Widget{Type: "text", X: x, Y: y, W: w, H: h, Opts: map[string]string{"text": text, "size": size, "align": align}}
	}
	return Layout{Version: 1, Pages: []Page{{Name: "Stock", BG: "#ffffff", Widgets: []Widget{
		{Type: "bars", X: 6, Y: 6, W: 26, H: 18},
		t(38, 4, 120, 22, "4G LTE", "mb", "left"),
		{Type: "battery", X: 200, Y: 4, W: 112, H: 22, Opts: map[string]string{"size": "mb", "align": "right"}},
		{Type: "operator", X: 16, Y: 44, W: 288, H: 28, Opts: map[string]string{"size": "l"}},
		t(8, 110, 100, 60, "Wi-Fi on", "m", "center"),
		{Type: "devices", X: 110, Y: 110, W: 100, H: 60, Opts: map[string]string{"size": "m", "align": "center"}},
		t(212, 110, 100, 60, "Wi-Fi name", "m", "center"),
		t(24, 198, 128, 34, "Menu", "mb", "center"),
		t(168, 198, 128, 34, "Help", "mb", "center"),
	}}}}
}

// readFramebuffer reads the current screen (read-only) using the geometry the kernel reports in sysfs.
func readFramebuffer(dev string) (*image.RGBA, error) {
	name := filepath.Base(dev)
	sys := "/sys/class/graphics/" + name + "/"
	size := strings.Split(readTrim(sys+"virtual_size"), ",")
	if len(size) != 2 {
		return nil, errNoFramebuffer
	}
	w, _ := strconv.Atoi(size[0])
	h, _ := strconv.Atoi(size[1])
	bpp, _ := strconv.Atoi(readTrim(sys + "bits_per_pixel"))
	stride, _ := strconv.Atoi(readTrim(sys + "stride"))
	f, err := os.Open(dev) // read-only: the mirror never writes to the screen
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, stride*h)
	n, err := readFull(f, raw)
	if err != nil {
		return nil, err
	}
	return decodeFramebuffer(raw[:n], w, h, stride, bpp)
}

func readFull(f *os.File, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := f.Read(b[n:])
		n += m
		if err != nil {
			if n == len(b) {
				return n, nil
			}
			return n, err
		}
		if m == 0 {
			break
		}
	}
	return n, nil
}

// registerScreenAPI adds the screen mirror, the layout store and the server-rendered preview.
func registerScreenAPI(mux *http.ServeMux, cfg Config, col *Collector, layouts *LayoutStore, shower *Shower) {
	sendPNG := func(w http.ResponseWriter, b []byte) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	}
	mux.HandleFunc("GET /api/screen/mirror.png", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Demo {
			if c := shower.DemoFrame(); c != nil {
				sendPNG(w, c.PNG())
				return
			}
			sendPNG(w, RenderPage(StockLikeLayout(), 0, col.ScreenData()).PNG())
			return
		}
		img, err := readFramebuffer(cfg.FB)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Cannot read the router's screen: " + err.Error()})
			return
		}
		c := &Canvas{img: img, clip: img.Bounds()}
		sendPNG(w, c.PNG())
	})
	mux.HandleFunc("GET /api/screen/layout", func(w http.ResponseWriter, r *http.Request) {
		l, from, persistent := layouts.Get()
		writeJSON(w, http.StatusOK, map[string]any{"layout": l, "from": from, "persistent": persistent, "widgets": widgetTypes})
	})
	// Show on router: pause the stock screen and draw the layout on the device for a while.
	mux.HandleFunc("POST /api/screen/show", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Layout  Layout `json:"layout"`
			Page    int    `json:"page"`
			Seconds int    `json:"seconds"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if err := shower.Start(req.Layout, req.Page, time.Duration(req.Seconds)*time.Second); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, shower.Session())
	})
	mux.HandleFunc("POST /api/screen/stop", func(w http.ResponseWriter, r *http.Request) {
		shower.Stop()
		writeJSON(w, http.StatusOK, shower.Session())
	})
	mux.HandleFunc("GET /api/screen/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, shower.Session())
	})
	mux.HandleFunc("GET /api/screen/default", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, DefaultLayout())
	})
	mux.HandleFunc("PUT /api/screen/layout", func(w http.ResponseWriter, r *http.Request) {
		var l Layout
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&l); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid layout"})
			return
		}
		saved, err := layouts.Set(l)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"saved": saved})
	})
	// The preview renders a layout sent by the designer (not saved), or the current one.
	mux.HandleFunc("POST /api/screen/preview", func(w http.ResponseWriter, r *http.Request) {
		var l Layout
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&l); err != nil || l.Validate() != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid layout"})
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		sendPNG(w, RenderPage(l, page, col.ScreenData()).PNG())
	})
	mux.HandleFunc("GET /api/screen/preview", func(w http.ResponseWriter, r *http.Request) {
		l, _, _ := layouts.Get()
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		sendPNG(w, RenderPage(l, page, col.ScreenData()).PNG())
	})
}
