package i18n

import (
	"testing"

	"colaunch/internal/notify"
)

// The keys notifications build at run time, from kinds, fields, events
// and errors, are all there in the three languages.
func TestNotifyKeys(t *testing.T) {
	keys := []string{"reason.schedule", "reason.idle", "reason.budget", "reason.floor", "reason.user", "reason.gone"}
	for _, k := range notify.Kinds {
		keys = append(keys, "notify.kind."+k, "notify.help."+k)
		for _, f := range notify.Fields(k) {
			keys = append(keys, "notify.field."+f.Key)
		}
	}
	for _, e := range notify.Events {
		keys = append(keys, "notify.event."+e)
	}
	for _, c := range []string{"kind", "missing", "bad_value", "bad_url", "insecure_url", "bad_header", "network", "timeout", "status", "rejected"} {
		keys = append(keys, "notify.err."+c)
	}
	for _, l := range []string{"default", "active", "time_sensitive", "passive", "critical"} {
		keys = append(keys, "notify.level."+l)
	}
	for k := range notifyStrings {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if !Has(k) {
			t.Errorf("%q is missing a translation", k)
		}
	}
}
