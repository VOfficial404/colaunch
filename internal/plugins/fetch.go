package plugins

// Getting tasks from the network: a link to a pack, the community index,
// and colaunch://import links.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// IndexURL is the community's index of tasks, in the repository's tasks
// folder.
const IndexURL = "https://raw.githubusercontent.com/VOfficial404/colaunch/main/tasks/index.json"

// maxIndex is the most the community index may hold.
const maxIndex = 1 << 20

// ErrNotHTTPS is the error of a link that isn't https.
var ErrNotHTTPS = errors.New("plugins: only https:// links can be imported")

// HTTPClient fetches tasks: within 20 seconds, following at most five
// redirects, all to https.
var HTTPClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("plugins: too many redirects")
		}
		if req.URL.Scheme != "https" {
			return ErrNotHTTPS
		}
		return nil
	},
}

var githubPath = regexp.MustCompile(`^/([^/]+)/([^/]+)/(?:blob|raw)/(.+)$`)

// RawURL checks that link is https and turns a GitHub page of a file,
// github.com/owner/repo/blob/branch/path, into the file itself on
// raw.githubusercontent.com.
func RawURL(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("plugins: %q isn't a link", link)
	}
	if u.Scheme != "https" {
		return "", ErrNotHTTPS
	}
	if u.User != nil {
		return "", fmt.Errorf("plugins: links with a user name or password aren't imported")
	}
	if strings.EqualFold(u.Hostname(), "github.com") {
		if m := githubPath.FindStringSubmatch(u.Path); m != nil {
			return (&url.URL{Scheme: "https", Host: "raw.githubusercontent.com", Path: "/" + m[1] + "/" + m[2] + "/" + m[3]}).String(), nil
		}
	}
	u.Fragment = ""
	return u.String(), nil
}

// Fetch downloads an https link of at most limit bytes.
func Fetch(ctx context.Context, link string, limit int64) ([]byte, error) {
	raw, err := RawURL(link)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugins: %s: %s", raw, resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, ErrTooBig
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooBig
	}
	return b, nil
}

// FetchPack downloads and reads the pack at an https link.
func FetchPack(ctx context.Context, link string) (*Pack, error) {
	b, err := Fetch(ctx, link, MaxPack)
	if err != nil {
		return nil, err
	}
	p, err := Parse(b)
	if err != nil {
		return nil, err
	}
	p.Source = strings.TrimSpace(link)
	return p, nil
}

// IndexEntry is a task of the community index.
type IndexEntry struct {
	ID          string `json:"id"`
	Name        Text   `json:"name"`
	Description Text   `json:"description,omitempty"`
	Author      string `json:"author,omitempty"`
	// Path is the pack's file, relative to the index.
	Path   string   `json:"path"`
	Tags   []string `json:"tags,omitempty"`
	SHA256 string   `json:"sha256"`
}

// Index is the community index's file.
type Index struct {
	Version int          `json:"version"`
	Tasks   []IndexEntry `json:"tasks"`
}

var (
	packPathPattern = regexp.MustCompile(`^[A-Za-z0-9_\-]+(/[A-Za-z0-9_\-]+)*\.colaunch\.json$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseIndex reads the community index: an object with "tasks", or a
// bare list. Entries that aren't right are left out.
func ParseIndex(b []byte) ([]IndexEntry, error) {
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		if err := json.Unmarshal(b, &idx.Tasks); err != nil {
			return nil, fmt.Errorf("plugins: the community index: %w", err)
		}
	}
	out := idx.Tasks[:0:0]
	for _, e := range idx.Tasks {
		e.SHA256 = strings.ToLower(e.SHA256)
		if idPattern.MatchString(e.ID) && packPathPattern.MatchString(e.Path) && sha256Pattern.MatchString(e.SHA256) {
			if len(e.Name) == 0 {
				e.Name = Text{"en": e.ID}
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// FetchIndex downloads the community index at link.
func FetchIndex(ctx context.Context, link string) ([]IndexEntry, error) {
	b, err := Fetch(ctx, link, maxIndex)
	if err != nil {
		return nil, err
	}
	return ParseIndex(b)
}

// URL is where the entry's pack is, beside the index at indexURL.
func (e IndexEntry) URL(indexURL string) (string, error) {
	base, err := url.Parse(indexURL)
	if err != nil {
		return "", err
	}
	if !packPathPattern.MatchString(e.Path) {
		return "", fmt.Errorf("plugins: %q isn't a pack's path", e.Path)
	}
	u := base.ResolveReference(&url.URL{Path: e.Path})
	if u.Scheme != "https" || u.Host != base.Host {
		return "", ErrNotHTTPS
	}
	return u.String(), nil
}

// CheckSHA256 checks that b's SHA-256 is want, in hex.
func CheckSHA256(b []byte, want string) error {
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != strings.ToLower(want) {
		return ErrChecksum
	}
	return nil
}

// FetchEntry downloads the pack of an entry of the index at indexURL,
// checks it against the entry's checksum and ID, and reads it.
func FetchEntry(ctx context.Context, indexURL string, e IndexEntry) (*Pack, error) {
	link, err := e.URL(indexURL)
	if err != nil {
		return nil, err
	}
	b, err := Fetch(ctx, link, MaxPack)
	if err != nil {
		return nil, err
	}
	if err := CheckSHA256(b, e.SHA256); err != nil {
		return nil, err
	}
	p, err := Parse(b)
	if err != nil {
		return nil, err
	}
	if p.Manifest.ID != e.ID {
		return nil, fmt.Errorf("%w: the pack's ID %q isn't the index's %q", ErrInvalid, p.Manifest.ID, e.ID)
	}
	p.Source = link
	return p, nil
}

// DeepLinkScheme is the scheme of links that import tasks:
// colaunch://import?url=https://… or colaunch://import?code=colaunch-task:….
const DeepLinkScheme = "colaunch"

// ParseDeepLink reads a colaunch://import link: the share code it holds,
// or the https link of the pack to fetch.
func ParseDeepLink(link string) (code, packURL string, err error) {
	if len(link) > 2*MaxPack {
		return "", "", ErrTooBig
	}
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.EqualFold(u.Scheme, DeepLinkScheme) {
		return "", "", fmt.Errorf("plugins: %q isn't a Colaunch link", link)
	}
	what := u.Host
	if what == "" {
		what = strings.Trim(u.Path, "/")
		if what == "" {
			what = strings.SplitN(u.Opaque, "?", 2)[0]
		}
	}
	if !strings.EqualFold(what, "import") {
		return "", "", fmt.Errorf("plugins: Colaunch links only import tasks, not %q", what)
	}
	q := u.Query()
	if c := strings.TrimSpace(q.Get("code")); c != "" {
		if !strings.HasPrefix(c, CodePrefix) {
			c = CodePrefix + c
		}
		return c, "", nil
	}
	if l := strings.TrimSpace(q.Get("url")); l != "" {
		if _, err := RawURL(l); err != nil {
			return "", "", err
		}
		return "", l, nil
	}
	return "", "", fmt.Errorf("plugins: the link has neither code nor url")
}
