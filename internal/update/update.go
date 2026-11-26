// Package update finds out whether a newer Colaunch is out: through MyGo's
// updater on desktops built with updates, which also installs it, and
// through GitHub's API of releases everywhere else, phones included. It
// keeps what it learned, the version the user skipped and GitHub's ETags
// in a file of its own.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where Colaunch's releases are.
const Repo = "VOfficial404/colaunch"

// DefaultAPI is GitHub's API.
const DefaultAPI = "https://api.github.com"

// maxBody is the most of a response read: the release list with its notes
// is well under it.
const maxBody = 2 << 20

// Info is what a check found.
type Info struct {
	// Current is the running version, Latest the newest release's, both
	// without a "v".
	Current string `json:"current"`
	Latest  string `json:"latest"`
	// Notes are the release notes, in Markdown.
	Notes string `json:"notes,omitempty"`
	// URL is the release's page.
	URL string `json:"url,omitempty"`
	// Download is the file to install on this platform, when the release
	// has one; Downloads are all that fit it, as Linux's packages.
	Download  string  `json:"download,omitempty"`
	Downloads []Asset `json:"downloads,omitempty"`
	// Available reports whether Latest is newer than Current.
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checked_at"`
	// Native reports that MyGo's updater found it, which installs it.
	Native bool `json:"native,omitempty"`
}

// Asset is a file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size,omitempty"`
}

// Native is MyGo's updater, which apps built with updates check and
// install through.
type Native interface {
	// Enabled reports whether the app can update itself.
	Enabled() bool
	// Check returns the newer version and its notes, or "" when the app is
	// up to date.
	Check(ctx context.Context) (version, notes string, err error)
}

// Config sets a Checker up.
type Config struct {
	// Current is the running version.
	Current string
	// GOOS and GOARCH choose the downloads.
	GOOS, GOARCH string
	// Repo is "owner/name"; Repo by default.
	Repo string
	// API is GitHub's API, DefaultAPI by default. It must be https.
	API string
	// Client makes the requests; one with a timeout by default.
	Client *http.Client
	// Native is used first when it is enabled; nil on phones.
	Native Native
	// Path is the file the checker keeps its state in; "" keeps none.
	Path string
	// Now is the clock, time.Now by default.
	Now func() time.Time
}

// RateLimitError is GitHub refusing requests for a while.
type RateLimitError struct{ Until time.Time }

func (e *RateLimitError) Error() string {
	return "GitHub's rate limit was reached until " + e.Until.Format(time.Kitchen)
}

// StatusError is an answer of GitHub other than a release.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("GitHub answered %d", e.Code) }

// ErrNoRelease is a repository without a release of the app.
var ErrNoRelease = errors.New("no release found")

// Checker checks for updates. Its methods may be called from any
// goroutine.
type Checker struct {
	cfg Config
	mu  sync.Mutex
	st  state
	// checking serializes checks.
	checking sync.Mutex
}

type state struct {
	Last    *Info            `json:"last,omitempty"`
	Skipped string           `json:"skipped,omitempty"`
	Cache   map[string]entry `json:"cache,omitempty"`
	// Until is when GitHub takes requests again; Backoff is the last wait,
	// doubled while it keeps refusing without saying for how long.
	Until   time.Time     `json:"until,omitzero"`
	Backoff time.Duration `json:"backoff,omitempty"`
}

type entry struct {
	ETag string `json:"etag"`
	Body string `json:"body"`
}

// New returns a checker, with the state its file has.
func New(cfg Config) *Checker {
	if cfg.Repo == "" {
		cfg.Repo = Repo
	}
	if cfg.API == "" {
		cfg.API = DefaultAPI
	}
	cfg.API = strings.TrimRight(cfg.API, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Current = strings.TrimPrefix(strings.TrimSpace(cfg.Current), "v")
	c := &Checker{cfg: cfg}
	if cfg.Path != "" {
		if b, err := os.ReadFile(cfg.Path); err == nil {
			json.Unmarshal(b, &c.st)
		}
	}
	return c
}

// Last returns what the last successful check found, compared with the
// running version, which may have changed since.
func (c *Checker) Last() (Info, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Last == nil {
		return Info{}, false
	}
	info := *c.st.Last
	info.Current = c.cfg.Current
	info.Available = Newer(info.Latest, info.Current)
	return info, true
}

// CheckedAt is when the last check succeeded, or the zero time.
func (c *Checker) CheckedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Last == nil {
		return time.Time{}
	}
	return c.st.Last.CheckedAt
}

// Skipped is the version the user chose to skip.
func (c *Checker) Skipped() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.Skipped
}

// Skip skips version: Offer is false for it, and true again for the next.
func (c *Checker) Skip(version string) {
	c.mu.Lock()
	c.st.Skipped = strings.TrimPrefix(version, "v")
	c.mu.Unlock()
	c.save()
}

// Offer reports whether info is a version to tell the user about: newer
// and not skipped.
func (c *Checker) Offer(info Info) bool {
	return info.Available && info.Latest != c.Skipped()
}

// Check finds the newest release.
func (c *Checker) Check(ctx context.Context) (Info, error) {
	c.checking.Lock()
	defer c.checking.Unlock()
	info, err := c.check(ctx)
	if err != nil {
		return Info{}, err
	}
	info.Current = c.cfg.Current
	info.CheckedAt = c.cfg.Now()
	c.mu.Lock()
	c.st.Last = &info
	c.mu.Unlock()
	c.save()
	return info, nil
}

func (c *Checker) check(ctx context.Context) (Info, error) {
	var nativeErr error
	if n := c.cfg.Native; n != nil && n.Enabled() {
		v, notes, err := n.Check(ctx)
		if err == nil {
			v = strings.TrimPrefix(v, "v")
			info := Info{Latest: c.cfg.Current, Native: true}
			if v != "" {
				info.Latest, info.Notes, info.Available = v, notes, true
				info.URL = c.releasePage("v" + v)
				// GitHub's release gives the page and the installer, for
				// those who rather download it; not needed to install.
				if gh, err := c.github(ctx); err == nil && gh.Latest == v {
					info.URL, info.Download, info.Downloads = gh.URL, gh.Download, gh.Downloads
					if info.Notes == "" {
						info.Notes = gh.Notes
					}
				}
			}
			return info, nil
		}
		nativeErr = err
	}
	info, err := c.github(ctx)
	if err != nil && nativeErr != nil {
		return Info{}, fmt.Errorf("%w (updater: %v)", err, nativeErr)
	}
	return info, err
}

func (c *Checker) releasePage(tag string) string {
	return "https://github.com/" + c.cfg.Repo + "/releases/tag/" + tag
}

// release is what GitHub's API says of one.
type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
	Body       string `json:"body"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// usable reports whether r is a release of the app: a published one,
// not a prerelease, tagged v and a semantic version.
func (r release) usable() bool {
	if r.Draft || r.Prerelease || !strings.HasPrefix(r.TagName, "v") {
		return false
	}
	v, ok := parse(r.TagName)
	return ok && len(v.pre) == 0
}

// github reads the latest release from GitHub's API. The latest one can
// be another kind, as the llama-cuda builds, so it then looks through the
// list.
func (c *Checker) github(ctx context.Context) (Info, error) {
	base := c.cfg.API + "/repos/" + c.cfg.Repo + "/releases"
	var r release
	found := false
	body, status, err := c.get(ctx, base+"/latest")
	switch {
	case err != nil:
		return Info{}, err
	case status == http.StatusOK:
		if err := json.Unmarshal(body, &r); err != nil {
			return Info{}, fmt.Errorf("GitHub's release: %w", err)
		}
		found = r.usable()
	case status != http.StatusNotFound:
		return Info{}, &StatusError{Code: status}
	}
	if !found {
		body, status, err := c.get(ctx, base+"?per_page=30")
		if err != nil {
			return Info{}, err
		}
		if status != http.StatusOK {
			return Info{}, &StatusError{Code: status}
		}
		var list []release
		if err := json.Unmarshal(body, &list); err != nil {
			return Info{}, fmt.Errorf("GitHub's releases: %w", err)
		}
		for _, l := range list {
			if l.usable() && (!found || Newer(l.TagName, r.TagName)) {
				r, found = l, true
			}
		}
		if !found {
			return Info{}, ErrNoRelease
		}
	}
	return c.fromRelease(r), nil
}

func (c *Checker) fromRelease(r release) Info {
	v := strings.TrimPrefix(r.TagName, "v")
	info := Info{Latest: v, Notes: Notes(r.Body), Available: Newer(v, c.cfg.Current)}
	info.URL = c.releasePage(r.TagName)
	if u, err := url.Parse(r.HTMLURL); err == nil && u.Scheme == "https" {
		info.URL = r.HTMLURL
	}
	var assets []Asset
	for _, a := range r.Assets {
		if u, err := url.Parse(a.URL); err == nil && u.Scheme == "https" {
			assets = append(assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		}
	}
	info.Downloads = Pick(assets, v, c.cfg.GOOS, c.cfg.GOARCH)
	if len(info.Downloads) > 0 {
		info.Download = info.Downloads[0].URL
	}
	return info
}

// get fetches url, with the ETag of the last answer, whose body it gives
// again when GitHub says it is unchanged.
func (c *Checker) get(ctx context.Context, rawURL string) ([]byte, int, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, 0, fmt.Errorf("refusing %s: only https", rawURL)
	}
	now := c.cfg.Now()
	c.mu.Lock()
	until := c.st.Until
	cached, haveCache := c.st.Cache[rawURL]
	c.mu.Unlock()
	if now.Before(until) {
		return nil, 0, &RateLimitError{Until: until}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Colaunch/"+c.cfg.Current)
	if haveCache && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}
	client := *c.cfg.Client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" {
			return errors.New("redirected away from https")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if limited(resp) {
		return nil, 0, c.limit(resp, now)
	}
	c.mu.Lock()
	c.st.Backoff = 0
	c.mu.Unlock()
	if resp.StatusCode == http.StatusNotModified && haveCache {
		return []byte(cached.Body), http.StatusOK, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxBody {
		return nil, 0, errors.New("GitHub's answer is too large")
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		c.mu.Lock()
		if c.st.Cache == nil {
			c.st.Cache = map[string]entry{}
		}
		c.st.Cache[rawURL] = entry{ETag: etag, Body: string(body)}
		c.mu.Unlock()
	}
	return body, http.StatusOK, nil
}

// limited reports whether GitHub refused a request for its rate limit.
func limited(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
	}
	return false
}

// limit waits as GitHub says, or longer each time it doesn't.
func (c *Checker) limit(resp *http.Response, now time.Time) error {
	var until time.Time
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		until = now.Add(time.Duration(s) * time.Second)
	} else if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && s > now.Unix() {
		until = time.Unix(s, 0)
	}
	c.mu.Lock()
	if until.IsZero() {
		b := c.st.Backoff * 2
		if b < 15*time.Minute {
			b = 15 * time.Minute
		}
		if b > 12*time.Hour {
			b = 12 * time.Hour
		}
		c.st.Backoff = b
		until = now.Add(b)
	}
	if until.Sub(now) < time.Minute {
		until = now.Add(time.Minute)
	}
	c.st.Until = until
	c.mu.Unlock()
	c.save()
	return &RateLimitError{Until: until}
}

func (c *Checker) save() {
	if c.cfg.Path == "" {
		return
	}
	c.mu.Lock()
	b, err := json.Marshal(c.st)
	c.mu.Unlock()
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(c.cfg.Path), 0o700)
	tmp := c.cfg.Path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, c.cfg.Path)
	}
}

// Notes are a release's notes without the table of downloads the release
// workflow puts first, which the app picks from itself.
func Notes(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "## Download") {
		return strings.TrimSpace(body)
	}
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "The other files are for automatic updates") {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
		if strings.HasPrefix(l, "#") {
			return strings.TrimSpace(strings.Join(lines[i:], "\n"))
		}
	}
	return ""
}

// Pick returns the assets of version v to install on goos/goarch, the one
// to recommend first.
func Pick(assets []Asset, v, goos, goarch string) []Asset {
	find := func(name string) []Asset {
		for _, a := range assets {
			if a.Name == name {
				return []Asset{a}
			}
		}
		return nil
	}
	switch goos {
	case "android":
		return find("colaunch-" + v + ".apk")
	case "ios":
		return find("colaunch-" + v + ".ipa")
	case "windows":
		return find("colaunch-setup-" + v + "-" + goarch + ".exe")
	case "darwin":
		return find("colaunch-" + v + "-" + goarch + ".dmg")
	case "linux":
		rpm := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
		appimage := map[string][]string{"amd64": {"x86_64", "amd64"}, "arm64": {"aarch64", "arm64"}}[goarch]
		var out []Asset
		out = append(out, find("colaunch_"+v+"_"+goarch+".deb")...)
		if rpm != "" {
			out = append(out, find("colaunch-"+v+"-1."+rpm+".rpm")...)
		}
		for _, a := range assets {
			if !strings.HasSuffix(a.Name, ".AppImage") || !strings.Contains(a.Name, v) {
				continue
			}
			for _, arch := range appimage {
				if strings.Contains(a.Name, arch) {
					out = append(out, a)
					break
				}
			}
		}
		return out
	}
	return nil
}

// version is a semantic version.
type version struct {
	num [3]int
	pre []string
}

func parse(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v version
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		if s[i+1:] == "" {
			return version{}, false
		}
		v.pre = strings.Split(s[i+1:], ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return version{}, false
		}
		v.num[i] = n
	}
	return v, true
}

// Compare compares two semantic versions, with or without "v": -1, 0 or
// 1; ok is false when either isn't one.
func Compare(a, b string) (int, bool) {
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i := range va.num {
		if va.num[i] != vb.num[i] {
			if va.num[i] < vb.num[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, true
	case len(va.pre) == 0:
		return 1, true
	case len(vb.pre) == 0:
		return -1, true
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		x, y := va.pre[i], vb.pre[i]
		if x == y {
			continue
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if nx < ny {
				return -1, true
			}
			return 1, true
		case ex == nil:
			return -1, true
		case ey == nil:
			return 1, true
		case x < y:
			return -1, true
		}
		return 1, true
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1, true
	case len(va.pre) > len(vb.pre):
		return 1, true
	}
	return 0, true
}

// Newer reports whether a is a newer version than b. A running version
// that isn't one, as "dev", is never updated.
func Newer(a, b string) bool {
	n, ok := Compare(a, b)
	return ok && n > 0
}
