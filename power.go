package main

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Battery details on the 8800L come from the fuel gauge (bq27520-battery: voltage_now in µV, current_now in µA,
// temp_ambient in 0.1 °C, time_to_empty_now in s) and the charger (smb1351-charger: charge_type, health).

// batteryCapacityMAh is the 8800L battery (4400 mAh), used only when the fuel gauge gives no time to empty.
const batteryCapacityMAh = 4400

type powerReading struct {
	voltageV   *float64
	currentMA  *float64 // raw, as the gauge reports it
	tteS       int64
	chargeType string
	health     string
}

func readPowerDetails(base string) powerReading {
	var r powerReading
	g := filepath.Join(base, "bq27520-battery")
	if v, err := strconv.ParseFloat(readTrim(filepath.Join(g, "voltage_now")), 64); err == nil && v > 0 {
		r.voltageV = fp(math.Round(v/1e3) / 1e3)
	}
	if v, err := strconv.ParseFloat(readTrim(filepath.Join(g, "current_now")), 64); err == nil {
		r.currentMA = fp(v / 1e3)
	}
	r.tteS, _ = strconv.ParseInt(readTrim(filepath.Join(g, "time_to_empty_now")), 10, 64)
	ch := filepath.Join(base, "smb1351-charger")
	r.chargeType = readTrim(filepath.Join(ch, "charge_type"))
	r.health = readTrim(filepath.Join(ch, "health"))
	return r
}

// batteryDrain smooths the current over about a minute, so the time-left estimate does not jump around.
type batteryDrain struct {
	ema float64
	at  time.Time
}

func (d *batteryDrain) add(ma float64, now time.Time) float64 {
	if d.at.IsZero() || now.Sub(d.at) > 2*time.Minute {
		d.ema = ma
	} else {
		a := math.Min(1, now.Sub(d.at).Seconds()/60)
		d.ema += (ma - d.ema) * a
	}
	d.at = now
	return d.ema
}

// fillPower adds voltage, current (positive = charging, negative = draining), power, charge phase and the
// time left on battery. The sign of current_now differs between gauge drivers, so it is set from the
// battery state rather than trusted: on battery the current always flows out.
func fillPower(b *Battery, r powerReading, drain *batteryDrain, now time.Time) {
	b.VoltageV = r.voltageV
	onBattery := b.ExtPower != nil && !*b.ExtPower
	if r.currentMA != nil {
		ma := *r.currentMA
		switch {
		case onBattery || b.Status == "discharging":
			ma = -math.Abs(ma)
		case b.Status == "charging":
			ma = math.Abs(ma)
		}
		b.CurrentMA = fp(math.Round(ma))
		if b.VoltageV != nil {
			b.PowerW = fp(math.Round(*b.VoltageV*ma/10) / 100)
		}
	}
	b.Phase = chargePhase(b, r.chargeType, onBattery)
	if r.health != "" && !strings.EqualFold(r.health, "Good") && !strings.EqualFold(r.health, "Unknown") {
		b.Health = r.health
	}
	if !onBattery || b.CurrentMA == nil {
		*drain = batteryDrain{}
		return
	}
	ma := drain.add(-*b.CurrentMA, now)
	switch {
	case r.tteS > 0:
		b.TimeLeftS = &r.tteS
	case ma > 20 && b.Percent != nil:
		s := int64(float64(batteryCapacityMAh) * float64(*b.Percent) / 100 / ma * 3600)
		b.TimeLeftS = &s
		b.TimeLeftEst = true
	}
}

func chargePhase(b *Battery, chargeType string, onBattery bool) string {
	switch {
	case onBattery:
		return "on battery"
	case b.Status == "full":
		return "full"
	case b.Status == "discharging":
		return "not charging"
	}
	switch strings.ToLower(chargeType) {
	case "fast":
		return "fast charging"
	case "taper":
		return "topping up"
	case "trickle":
		return "trickle charging"
	}
	if b.Status == "charging" {
		return "charging"
	}
	return ""
}

// parseDsmState reads `dsm_cli get_state`: "key: [value]" lines.
func parseDsmState(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, ": [")
		j := strings.LastIndexByte(line, ']')
		if i <= 0 || j < i {
			continue
		}
		m[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+3 : j])
	}
	return m
}

// powerOnCauseText explains dsm's power_on_cause in plain words.
func powerOnCauseText(c string) string {
	switch strings.ToLower(c) {
	case "":
		return ""
	case "power_button":
		return "Power button"
	case "usb", "charger", "charger_insert", "usb_insert", "ext_power":
		return "Power was connected"
	case "reboot", "soft_reset", "software_reset", "sw_reset":
		return "Restart"
	case "watchdog", "wdog":
		return "Watchdog restart"
	case "hard_reset", "reset":
		return "Reset"
	}
	s := strings.ReplaceAll(c, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}
