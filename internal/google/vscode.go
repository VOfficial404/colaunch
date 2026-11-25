package google

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	clientIDPattern     = regexp.MustCompile(`[0-9]+-[0-9a-z]+\.apps\.googleusercontent\.com`)
	clientSecretPattern = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_\-]{20,40}`)
)

// editorDirs are where VS Code and its forks keep their extensions.
var editorDirs = []string{
	".vscode", ".vscode-insiders", ".vscode-oss", ".vscode-server",
	".cursor", ".windsurf", ".antigravity", ".trae", ".kiro",
}

// FindEditorClient looks for the OAuth client of Google's Colab extension
// in the editors installed on this machine, for users who'd rather reuse it
// than register a client of their own. It returns ErrNoClient when there
// is none.
func FindEditorClient() (Client, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Client{}, "", err
	}
	var dirs []string
	for _, e := range editorDirs {
		matches, _ := filepath.Glob(filepath.Join(home, e, "extensions", "google.colab-*"))
		dirs = append(dirs, matches...)
	}
	// The newest version first.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs {
		if c, err := scanClient(dir); err == nil {
			return c, dir, nil
		}
	}
	return Client{}, "", ErrNoClient
}

func scanClient(dir string) (Client, error) {
	var found Client
	stop := errors.New("found")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".js") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 64<<20 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		id := clientIDPattern.Find(b)
		if id == nil {
			return nil
		}
		found.ID = string(id)
		if s := clientSecretPattern.Find(b); s != nil {
			found.Secret = string(s)
		}
		return stop
	})
	if errors.Is(err, stop) && found.Valid() {
		return found, nil
	}
	return Client{}, ErrNoClient
}
