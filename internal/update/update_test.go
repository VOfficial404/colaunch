package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type rel struct {
	Tag        string
	Draft, Pre bool
	Body       string
	Assets     []string
}

func (r rel) json(base string) map[string]any {
	var assets []map[string]any
	for _, a := range r.Assets {
		assets = append(assets, map[string]any{"name": a, "browser_download_url": "https://github.com/x/releases/download/" + r.Tag + "/" + a, "size": 10})
	}
	return map[string]any{"tag_name": r.Tag, "draft": r.Draft, "prerelease": r.Pre, "body": r.Body,
		"html_url": "https://github.com/VOfficial404/colaunch/releases/tag/" + r.Tag, "assets": assets}
}

var allAssets = []string{
	"colaunch-0.2.0.apk", "colaunch-0.2.0.ipa",
	"colaunch-setup-0.2.0-amd64.exe", "colaunch-setup-0.2.0-arm64.exe",
	"colaunch-0.2.0-arm64.dmg", "colaunch-0.2.0-amd64.dmg",
	"colaunch_0.2.0_amd64.deb", "colaunch_0.2.0_arm64.deb",
	"colaunch-0.2.0-1.x86_64.rpm", "colaunch-0.2.0-1.aarch64.rpm",
	"Colaunch-0.2.0-x86_64.AppImage", "update-windows-amd64.json",
}

// server serves latest and the list; it counts requests and honours ETags.
func server(t *testing.T, latest rel, list []rel) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var v any
		switch r.URL.Path {
		case "/repos/VOfficial404/colaunch/releases/latest":
			v = latest.json("")
		case "/repos/VOfficial404/colaunch/releases":
			var out []any
			for _, l := range list {
				out = append(out, l.json(""))
			}
			v = out
		default:
			http.NotFound(w, r)
			return
		}
		b, _ := json.Marshal(v)
		etag := fmt.Sprintf(`"%x"`, len(b))
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newChecker(srv *httptest.Server, cur, goos, goarch, path string) *Checker {
	return New(Config{Current: cur, GOOS: goos, GOARCH: goarch, API: srv.URL, Client: srv.Client(), Path: path})
}

func TestPlatforms(t *testing.T) {
	srv, _ := server(t, rel{Tag: "v0.2.0", Body: "## Download · 下载\n\n| a | b |\n\nThe other files are for automatic updates and install scripts: you don't need them.\n\n- Fixed things", Assets: allAssets}, nil)
	for _, tc := range []struct{ goos, arch, want string }{
		{"android", "arm64", "colaunch-0.2.0.apk"},
		{"ios", "arm64", "colaunch-0.2.0.ipa"},
		{"windows", "amd64", "colaunch-setup-0.2.0-amd64.exe"},
		{"windows", "arm64", "colaunch-setup-0.2.0-arm64.exe"},
		{"darwin", "arm64", "colaunch-0.2.0-arm64.dmg"},
		{"linux", "amd64", "colaunch_0.2.0_amd64.deb"},
	} {
		info, err := newChecker(srv, "0.1.7", tc.goos, tc.arch, "").Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !info.Available || info.Latest != "0.2.0" || info.Current != "0.1.7" {
			t.Errorf("%s: %+v", tc.goos, info)
		}
		if want := "https://github.com/x/releases/download/v0.2.0/" + tc.want; info.Download != want {
			t.Errorf("%s/%s: download %q, want %q", tc.goos, tc.arch, info.Download, want)
		}
		if info.Notes != "- Fixed things" {
			t.Errorf("notes %q", info.Notes)
		}
	}
	info, _ := newChecker(srv, "0.1.7", "linux", "amd64", "").Check(context.Background())
	if len(info.Downloads) != 3 {
		t.Errorf("linux downloads %+v", info.Downloads)
	}
	info, _ = newChecker(srv, "0.2.0", "android", "arm64", "").Check(context.Background())
	if info.Available {
		t.Error("the running version is offered")
	}
}

// The latest release can be a llama-cuda build, or a draft, or a
// prerelease: the newest release of the app is found in the list.
func TestSkipsOtherReleases(t *testing.T) {
	srv, _ := server(t, rel{Tag: "llama-cuda-b5000"}, []rel{
		{Tag: "llama-cuda-b5000"},
		{Tag: "v0.4.0", Draft: true},
		{Tag: "v0.3.0-beta.1", Pre: true},
		{Tag: "v0.3.1-rc.1"},
		{Tag: "v0.2.1", Assets: []string{"colaunch-0.2.1.apk"}},
		{Tag: "v0.10.0", Assets: []string{"colaunch-0.10.0.apk"}},
		{Tag: "v0.9.0"},
	})
	info, err := newChecker(srv, "0.1.7", "android", "arm64", "").Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Latest != "0.10.0" || !info.Available || info.Download == "" {
		t.Errorf("%+v", info)
	}
}

func TestETagAndState(t *testing.T) {
	srv, hits := server(t, rel{Tag: "v0.2.0", Body: "notes", Assets: allAssets}, nil)
	path := filepath.Join(t.TempDir(), "update.json")
	c := newChecker(srv, "0.1.7", "android", "arm64", path)
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Another run reads the ETag from the file and gets 304.
	c = newChecker(srv, "0.1.7", "android", "arm64", path)
	info, err := c.Check(context.Background())
	if err != nil || info.Notes != "notes" || !info.Available {
		t.Fatalf("%+v %v", info, err)
	}
	if hits.Load() != 2 {
		t.Errorf("%d requests", hits.Load())
	}
	c.Skip("v0.2.0")
	c = newChecker(srv, "0.1.7", "android", "arm64", path)
	last, ok := c.Last()
	if !ok || c.Offer(last) || c.Skipped() != "0.2.0" || c.CheckedAt().IsZero() {
		t.Errorf("skipped version offered: %+v", last)
	}
	// A newer one is offered again.
	if !c.Offer(Info{Latest: "0.2.1", Available: true}) {
		t.Error("the next version isn't offered")
	}
	// After updating, the last check isn't an update any more.
	c = newChecker(srv, "0.2.0", "android", "arm64", path)
	if last, _ := c.Last(); last.Available {
		t.Error("the version running is offered")
	}
}

func TestRateLimit(t *testing.T) {
	var hits atomic.Int32
	reset := time.Now().Add(30 * time.Minute).Unix()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := newChecker(srv, "0.1.7", "ios", "arm64", "")
	_, err := c.Check(context.Background())
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Until.Unix() != reset {
		t.Fatalf("%v", err)
	}
	// It waits instead of asking again.
	if _, err := c.Check(context.Background()); !errors.As(err, &rl) || hits.Load() != 1 {
		t.Fatalf("asked again: %v, %d", err, hits.Load())
	}

	srv2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv2.Close()
	now := time.Now()
	c = New(Config{Current: "0.1.7", GOOS: "ios", API: srv2.URL, Client: srv2.Client(), Now: func() time.Time { return now }})
	if _, err := c.Check(context.Background()); !errors.As(err, &rl) || rl.Until.Sub(now) != 15*time.Minute {
		t.Fatalf("%v", err)
	}
	now = now.Add(16 * time.Minute)
	if _, err := c.Check(context.Background()); !errors.As(err, &rl) || rl.Until.Sub(now) != 30*time.Minute {
		t.Fatalf("no longer backoff: %v", err)
	}
}

func TestOnlyHTTPS(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := New(Config{Current: "0.1.7", GOOS: "android", API: srv.URL})
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("checked over http")
	}
}

func TestTooLarge(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, maxBody+10))
	}))
	defer srv.Close()
	if _, err := newChecker(srv, "0.1.7", "android", "arm64", "").Check(context.Background()); err == nil {
		t.Fatal("read an answer over the limit")
	}
}

type fakeNative struct {
	enabled bool
	version string
	err     error
}

func (f fakeNative) Enabled() bool { return f.enabled }
func (f fakeNative) Check(context.Context) (string, string, error) {
	return f.version, "native notes", f.err
}

func TestNative(t *testing.T) {
	srv, hits := server(t, rel{Tag: "v0.2.0", Body: "gh notes", Assets: allAssets}, nil)
	mk := func(n Native) *Checker {
		return New(Config{Current: "0.1.7", GOOS: "windows", GOARCH: "amd64", API: srv.URL, Client: srv.Client(), Native: n})
	}
	info, err := mk(fakeNative{enabled: true}).Check(context.Background())
	if err != nil || info.Available || !info.Native || hits.Load() != 0 {
		t.Fatalf("up to date: %+v %v, %d requests", info, err, hits.Load())
	}
	info, err = mk(fakeNative{enabled: true, version: "0.2.0"}).Check(context.Background())
	if err != nil || !info.Available || !info.Native || info.Notes != "native notes" || info.Download == "" {
		t.Fatalf("update: %+v %v", info, err)
	}
	// Builds that can't update themselves, and failures, ask GitHub.
	info, err = mk(fakeNative{}).Check(context.Background())
	if err != nil || !info.Available || info.Native || info.Notes != "gh notes" {
		t.Fatalf("disabled: %+v %v", info, err)
	}
	info, err = mk(fakeNative{enabled: true, err: errors.New("offline")}).Check(context.Background())
	if err != nil || !info.Available || info.Native {
		t.Fatalf("failed: %+v %v", info, err)
	}
}

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.10.0", "0.9.9", 1}, {"v1.2.3", "1.2.3", 0}, {"1.3.0-beta.2", "1.3.0-beta.10", -1},
		{"1.3.0-beta.10", "1.3.0", -1}, {"1.3.0-alpha", "1.3.0-beta", -1}, {"1.3.0-1", "1.3.0-alpha", -1},
		{"1.3.0-alpha", "1.3.0-alpha.1", -1}, {"2.0.0+build", "2.0.0", 0},
	} {
		if got, ok := Compare(tc.a, tc.b); !ok || got != tc.want {
			t.Errorf("Compare(%s, %s) = %d %v", tc.a, tc.b, got, ok)
		}
	}
	if Newer("0.2.0", "dev") || Newer("llama-cuda-1", "0.1.0") {
		t.Error("compared a non-version")
	}
}
