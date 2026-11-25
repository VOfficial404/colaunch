package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Keys built at run time, as "phase." + a session's phase.
var dynamic = []string{
	"phase.validating", "phase.assigning", "phase.connecting", "phase.retrying", "phase.guarding",
	"phase.deploying", "phase.forwarding", "phase.running", "phase.reattaching", "phase.stopping",
	"phase.stopped", "phase.failed",
	"pphase.resolve", "pphase.llama", "pphase.build", "pphase.download", "pphase.start", "pphase.ready",
	"reason.user", "reason.manual", "reason.schedule", "reason.idle", "reason.budget", "reason.floor",
	"reason.gone", "reason.failed",
	"info.model", "info.quant", "info.size", "info.context", "info.offload",
}

// Every string the interface asks for is there in all three languages,
// so no one reads a key in place of words.
func TestEveryKeyTranslated(t *testing.T) {
	files, _ := filepath.Glob("../app/*.go")
	re := regexp.MustCompile(`(?:i18n\.[TF]\(|key:\s*|"nav\.)"?([a-z_]+\.[a-z_.]+)"`)
	keys := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			k := m[1]
			if k[len(k)-1] == '.' {
				continue // a prefix of dynamic keys
			}
			keys[k] = true
		}
	}
	for _, k := range dynamic {
		keys[k] = true
	}
	if len(keys) < 50 {
		t.Fatalf("found only %d keys: is the pattern right?", len(keys))
	}
	for k := range keys {
		if !Has(k) {
			t.Errorf("%q is missing a translation", k)
		}
	}
}

var verbs = regexp.MustCompile(`%[-+# 0]*[0-9.]*[a-zA-Z]`)

// A translation takes the same arguments as the English, or Sprintf
// writes %!(EXTRA …) into the interface.
func TestVerbsMatch(t *testing.T) {
	for k, s := range dict {
		want := len(verbs.FindAllString(s[0], -1))
		for i := 1; i < 3; i++ {
			if got := len(verbs.FindAllString(s[i], -1)); got != want {
				t.Errorf("%s[%d] has %d verbs, English %d", k, i, got, want)
			}
		}
	}
}
