# MiFi Panel

A custom web UI for the Inseego MiFi 8800L that replaces the stock one. It is a single file with no
dependencies that runs on the router itself and opens from any phone or laptop on its Wi-Fi.

What it does (version 0.5):

- live signal with a quality rating and a 15-minute chart: RSRP, SINR and RSRQ on LTE,
  RSCP and Ec/Io on 3G (the panel switches by itself), plus RSSI;
- antenna aiming mode: a full-screen number, how far you are from the best position so far, and a tone
  that rises as the signal improves;
- current internet speed with a chart, IP address, total traffic;
- whether the data session is up and for how long;
- **Reconnect** (what the manual network search in the stock menu does, in one tap),
  **Restart data session** (a new IP address, the way the stock software does it) and **Find all networks**;
- a **Cells** page: the serving cell (band, downlink frequency, EARFCN, PCI, cell ID, eNB ID and sector,
  MCC/MNC for looking the tower up on CellMapper), the LTE cells the modem hears nearby with their signal,
  a table of all networks from Find all networks, and a log of connection events (network type, band,
  cell, registration, data session, IP address);
- a **Screen** page: a live mirror of the router's own 2.4" screen, and a designer for your own screens
  (pages of widgets: big signal value, band and cell, signal bars, speed, traffic, battery, clock, operator,
  devices, signal chart, antenna aiming, data session, free text) with a pixel-exact preview drawn by the router,
  and **Show on router**: the router's screen shows your pages for a set time (tap for the next page, hold or
  press the power button to go back), after which the stock screen returns by itself. A failsafe process keeps
  the router's watchdog satisfied and brings the stock screen back even if the panel stops;
- who is connected to the Wi-Fi, battery level, uptime;
- a modem console for AT commands (off by default, for experiments).

## Preview without the router

Open `web/index.html` in a browser. It switches to demo mode with made-up data, and every button
and chart works. Add `?demo=3g` to the address to see how it looks on a 3G network, and `#cells` or
`#screen` to open those pages. Screen pictures are drawn by the panel, so for those run `mifi-panel -demo`.

## Step 1. Connect over ADB

1. Install Android platform-tools (the official archive from Google), unpack it and open a terminal there.
2. Connect the 8800L to the computer with a USB cable.
3. Run `adb devices`.

If the device is listed, you are good. If not, check what Device Manager shows: newer firmware versions
close ADB access, and then a different route is needed.

**Do not update the router firmware** while working on this: updates close ADB.

## Step 2. Collect information about the device

The script only reads. It changes nothing and sends no commands to the modem.

```
adb push probe.sh /tmp/
adb shell sh /tmp/probe.sh /tmp/probe.txt
adb pull /tmp/probe.txt
```

`probe.txt` shows the system, storage, network, running services and battery data.

Then the focused modem survey and the AT port check. Both are read-only: the check sends only `ATI`
and `AT+CSQ`, skips any port the stock software has open, and gives up on a port that does not open
or answer within a few seconds.

```
adb push probe-modem.sh /tmp/
adb shell sh /tmp/probe-modem.sh /tmp/probe-modem.txt
adb pull /tmp/probe-modem.txt

adb push dist/mifi-panel /tmp/
adb shell chmod 755 /tmp/mifi-panel
adb shell /tmp/mifi-panel -probe-at > probe-at.txt
```

Together they show which port talks to the modem, which vendor tools can report band and SINR,
and how the stock software is wired.

## Step 3. Try the panel on the router

For now the panel runs from RAM: after the router reboots it is gone, so nothing can break.

```
adb push dist/mifi-panel /tmp/
adb shell chmod 755 /tmp/mifi-panel
adb shell /tmp/mifi-panel -demo
```

From a phone or laptop on the router's Wi-Fi, open `http://192.168.1.1:8080`.
Or run `adb forward tcp:8080 tcp:8080` in a second window and open `http://localhost:8080` on the computer.

By default the panel listens only on the router's Wi-Fi address and on loopback, never on the LTE side:
the stock firewall has no rules for incoming connections.

Without `-demo` the panel shows real data. On the 8800L it needs no extra options: signal, network,
data session and devices come from the router's own tools in `/opt/nvtl/bin` (the same source as the
stock web UI), and the modem AT port `/dev/smd8` is used for the rest and for the Reconnect and
Find all networks buttons. Run it with a password once it talks to the real modem:

```
adb shell /tmp/mifi-panel -password choose-one
```

Stop the panel with Ctrl+C in the window where it runs.

## Options

| Option | What it does |
|---|---|
| `-listen lan` | `lan` (default) = Wi-Fi bridge and loopback only; or a list of addresses such as `:8080` |
| `-port 8080` | port for `-listen lan` |
| `-lan-iface br0` | Wi-Fi bridge interface for `-listen lan` |
| `-probe-at` | check which modem ports answer AT queries (read-only) and exit |
| `-demo` | made-up data, for testing |
| `-at auto` | modem AT port: `auto` (default) = `/dev/smd8` on the 8800L if no other program uses it; a path; or `""` for none |
| `-vendor-dir /opt/nvtl/bin` | where the router's own tools are (`modem2_cli`, `ccm2_cli`, `dmdb_cli`) |
| `-fb /dev/fb0` | the router's screen, read for the mirror |
| `-layout-dir /data/mifi-panel` | keep the screen layout across restarts (writes to flash); empty = memory only |
| `-radio-interval 4s` | how often to read the modem: signal, data session, devices |
| `-wan rmnet_data0` | LTE interface, if auto-detection picks the wrong one |
| `-password ...` | login password, user `admin` |
| `-at-console` | show the modem console in the UI |
| `-interval 2s` | how often to refresh speed, battery and system data |

Once the panel talks to the real modem, set a password and keep the modem console off unless you need
it: it can change modem settings.

## Development

Requires Go 1.22 or newer. `sh build.sh` vets the code, runs the tests and builds
`dist/mifi-panel` for the router (ARMv7) and `dist/mifi-panel-local` for your own computer.
`dist/mifi-panel-local -demo` serves the demo at `http://localhost:8080`.

How it is put together:

- `main.go`: web server and API (`/api/status`, `/api/history`, `/api/cells`, `/api/events`, `/api/info`,
  `/api/action`, `/api/at`);
- `render.go`: draws 320x240 frames (bitmap fonts, lines, text) and encodes them as PNG or as the
  framebuffer's RGB666; decodes the framebuffer for the mirror;
- `screen.go`: screen layouts, widgets, the default layout and the `/api/screen/...` endpoints;
- `fonts/`: Terminus bitmap fonts (SIL OFL 1.1, see `fonts/Terminus-OFL.txt`) with Latin and Cyrillic,
  made by `go run ./tools/fontgen <out.bin> <font.bdf>` from the Terminus 4.49.1 BDF files;
- `cells.go`: LTE channel table (EARFCN to band and MHz), the nearby cell list, serving cell details and
  the connection event log;
- `status.go`: data collection on an interval, history, modem actions in the background;
- `vendor.go`: reads the router's own tools (`modem2_cli`, `ccm2_cli`, `dmdb_cli`) and runs only the
  subcommands on a fixed allow list;
- `at.go`: talks to the modem with AT commands (3GPP and Qualcomm `$QC...`);
- `modemio.go`: non-blocking modem port access, so a port that never answers cannot hang the panel;
- `sys.go`: traffic, battery, clients (ISC dhcpd or dnsmasq leases) and system info from `/proc` and `/sys`;
- `probeat.go`: the read-only `-probe-at` check;
- `demo.go`: made-up data;
- `web/index.html`: the whole UI; the Geologica font (OFL license) is in `web/fonts`.

`CLAUDE.md` holds the project context and safety rules for Claude Code.
