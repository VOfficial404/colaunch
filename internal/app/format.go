package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"colaunch/internal/i18n"
)

// duration writes a duration as people read it: "2h 05m", "12m", "40s".
func duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h >= 48:
		return i18n.F("dur.days", h/24, h%24)
	case h > 0:
		return i18n.F("dur.hours", h, m)
	case m > 0:
		return i18n.F("dur.minutes", m)
	}
	return i18n.F("dur.seconds", int(d.Seconds()))
}

// minutes is a number of minutes as a duration.
func minutes(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }

// countdown writes the time left as a countdown reads: "17 s", "2:05",
// "1:02:05".
func countdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s >= 3600:
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	case s >= 60:
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return i18n.F("dur.seconds", s)
}

func gbFromMB(mb int64) string {
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

func fmtInt(n int) string { return fmt.Sprint(n) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func writeSecretFile(path string, m map[string]string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// appVersion is the version of mygo.json the build has, or Version.
func appVersion() string {
	if v := mygo.App.Version(); v != "" {
		return v
	}
	return Version
}
