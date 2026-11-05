// mifi-panel is a custom web UI for the MiFi 8800L (and other Linux routers with an LTE modem).
//
// It is a single static binary with no dependencies: it cross-compiles to ARMv7 and runs on the device itself.
// The -demo flag generates realistic fake data so the UI can be developed without the device.
package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const version = "0.6.1"

//go:embed web
var webFS embed.FS

type Config struct {
	Listen    string
	Port      int
	LANIface  string
	ProbeAT   bool
	Demo      bool
	ATPath    string
	WAN       string
	Leases    string
	Interval  time.Duration
	Radio     time.Duration
	VendorDir string
	FB        string
	TestFrame time.Duration
	Failsafe  string
	LayoutDir string
	Password  string
	ATConsole bool
}

func parseFlags() Config {
	var c Config
	flag.StringVar(&c.Listen, "listen", "lan", `where to listen: "lan" = loopback and the Wi-Fi bridge only (never the LTE side), or a comma-separated list of addresses such as ":8080"`)
	flag.IntVar(&c.Port, "port", 8080, `port used with -listen lan`)
	flag.StringVar(&c.LANIface, "lan-iface", "br0", `LAN bridge interface used with -listen lan`)
	flag.BoolVar(&c.ProbeAT, "probe-at", false, "check which modem ports answer AT queries (read-only), print a report and exit; optional port paths as arguments")
	flag.BoolVar(&c.Demo, "demo", false, "demo mode with made-up data (for development without the device)")
	flag.StringVar(&c.ATPath, "at", "auto", `modem AT port, e.g. /dev/smd8; "auto" = /dev/smd8 on the MiFi 8800L if no other program holds it; "" = none`)
	flag.StringVar(&c.WAN, "wan", "", "LTE network interface, e.g. rmnet_data0 (empty = auto-detect)")
	flag.StringVar(&c.Leases, "leases", "", "dnsmasq DHCP leases file (empty = search common paths)")
	flag.DurationVar(&c.Interval, "interval", 2*time.Second, "how often to refresh traffic and system data")
	flag.DurationVar(&c.Radio, "radio-interval", 4*time.Second, "how often to read the modem (signal, data session, devices)")
	flag.StringVar(&c.FB, "fb", "/dev/fb0", "the device screen (framebuffer); read for the screen mirror")
	flag.StringVar(&c.Failsafe, "screen-failsafe", "", "internal: run as the Show on router failsafe (pid,deadline,lcd)")
	flag.DurationVar(&c.TestFrame, "test-frame", 0, "draw a test pattern on the device screen for this long, restore the screen and exit (e.g. 5s)")
	flag.StringVar(&c.LayoutDir, "layout-dir", "", `where to keep the screen layout across restarts, e.g. /data/mifi-panel ("" = memory only)`)
	flag.StringVar(&c.VendorDir, "vendor-dir", "/opt/nvtl/bin", "where the stock MiFi tools (modem2_cli, ccm2_cli, dmdb_cli) are")
	flag.StringVar(&c.Password, "password", "", "login password (user admin); empty = no password")
	flag.BoolVar(&c.ATConsole, "at-console", false, "enable the modem AT console in the web UI (for experiments)")
	flag.Parse()
	if c.Interval < 500*time.Millisecond {
		c.Interval = 500 * time.Millisecond
	}
	if c.Radio < 2*time.Second {
		c.Radio = 2 * time.Second
	}
	if c.ATPath == "auto" {
		c.ATPath = autoATPort(c)
	}
	return c
}

// autoATPort picks /dev/smd8 on the MiFi 8800L (where the vendor tools exist), the port that answers AT
// and that no stock program holds. Elsewhere, and in demo mode, there is no default.
func autoATPort(c Config) string {
	const p = "/dev/smd8"
	if c.Demo || c.ProbeAT || NewVendor(c.VendorDir) == nil {
		return ""
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	if h := deviceHolders()[p]; len(h) > 0 {
		log.Printf("not using %s for AT: held by %s", p, strings.Join(h, ", "))
		return ""
	}
	return p
}

func main() {
	cfg := parseFlags()
	if cfg.ProbeAT {
		runProbeAT(flag.Args())
		return
	}
	if cfg.Failsafe != "" {
		runScreenFailsafe(cfg.Failsafe, cfg)
		return
	}
	if cfg.TestFrame > 0 {
		if err := runTestFrame(cfg.FB, min(cfg.TestFrame, 30*time.Second)); err != nil {
			log.Fatal(err)
		}
		return
	}
	col := NewCollector(cfg)
	go col.Run()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(sub))

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, col.Status())
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, col.HistorySnapshot())
	})
	mux.HandleFunc("GET /api/cells", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, col.CellsView())
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": col.Events()})
	})
	layouts := NewLayoutStore(cfg.LayoutDir)
	registerScreenAPI(mux, cfg, col, layouts, NewShower(cfg, col))
	registerWidgetAPI(mux)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"version":    version,
			"demo":       cfg.Demo,
			"at":         cfg.ATPath,
			"vendor":     col.vendor != nil,
			"at_console": cfg.ATConsole,
			"interval":   cfg.Interval.Milliseconds(),
		})
	})
	mux.HandleFunc("POST /api/action", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		if err := col.StartAction(req.Name); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"ok": req.Name})
	})
	if cfg.ATConsole {
		mux.HandleFunc("POST /api/at", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Cmd string `json:"cmd"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
				return
			}
			lines, err := col.RawAT(req.Cmd)
			resp := map[string]any{"lines": lines}
			if err != nil {
				resp["error"] = err.Error()
			}
			writeJSON(w, http.StatusOK, resp)
		})
	}

	handler := withAuth(cfg.Password, mux)
	mode := "live"
	if cfg.Demo {
		mode = "demo"
	}
	log.Printf("mifi-panel %s (%s mode, AT port %q, vendor tools %v)", version, mode, cfg.ATPath, col.vendor != nil)
	serve(cfg, handler)
}

func listenOn(addr string, h http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on http://%s", addr)
	return srv.ListenAndServe()
}

// serve listens on the configured addresses. In "lan" mode it binds only to loopback (for adb forward)
// and to the LAN bridge address, so the panel is never reachable from the LTE side even though
// the stock firewall has no INPUT rules. The bridge may get its address after boot, so we wait for it.
func serve(cfg Config, h http.Handler) {
	if cfg.Listen != "lan" {
		for _, a := range strings.Split(cfg.Listen, ",") {
			a := strings.TrimSpace(a)
			go func() { log.Fatal(listenOn(a, h)) }()
		}
		select {}
	}
	go func() { log.Fatal(listenOn(fmt.Sprintf("127.0.0.1:%d", cfg.Port), h)) }()
	go func() {
		waiting, lastErr := false, ""
		for {
			if _, addr := ifaceState(cfg.LANIface); addr != "" {
				err := listenOn(fmt.Sprintf("%s:%d", addr, cfg.Port), h)
				if e := err.Error(); e != lastErr {
					log.Printf("LAN listener: %v (will retry)", err)
					lastErr = e
				}
			} else if !waiting {
				log.Printf("waiting for %s to get an address", cfg.LANIface)
				waiting = true
			}
			time.Sleep(5 * time.Second)
		}
	}()
	select {}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withAuth(password string, next http.Handler) http.Handler {
	if password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="mifi-panel", charset="UTF-8"`)
			http.Error(w, "password required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// trimAT accepts only commands that start with AT and strips line breaks.
func trimAT(cmd string) (string, bool) {
	cmd = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(cmd, "\r", ""), "\n", ""))
	if len(cmd) < 2 || !strings.EqualFold(cmd[:2], "AT") {
		return "", false
	}
	return cmd, true
}
