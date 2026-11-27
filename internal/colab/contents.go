package colab

import (
	"context"
	"encoding/base64"
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

// Match the plugin manifest and runtime agent's directory naming rules.
var contentsPluginID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)
var contentsWaitNonce = regexp.MustCompile(`^[a-f0-9]{32}$`)

// The Contents API returns a whole JSON file model, with no tail option.
const (
	maxContentsLog    = 4 << 20
	maxWaitLog        = 16 << 10
	maxContentsStatus = 64 << 10
)

// ErrRuntimeProxyAuth indicates that the file API refused the proxy token.
var ErrRuntimeProxyAuth = errors.New("runtime proxy refused credentials")

// PluginLog reads only the plugin's log through the runtime's Contents API.
func (c *Client) PluginLog(ctx context.Context, conn *Connection, plugin string) (string, error) {
	log, err := c.pluginContents(ctx, conn, plugin, "log.txt", "log", maxContentsLog)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(log, conn.Token, "[redacted]"), nil
}

// WaitStatus is a launch-specific status and the byte range of its log tail.
type WaitStatus struct {
	Nonce     string
	UpdatedAt int64
	State     string
	Size      int64
	From      int64
	Log       []byte
}

// PluginWaitStatus reads the status file for one monitored launch.
func (c *Client) PluginWaitStatus(ctx context.Context, conn *Connection, plugin, nonce string) (WaitStatus, error) {
	const source = "colab: plugin status via Jupyter Contents"
	if !contentsWaitNonce.MatchString(nonce) {
		return WaitStatus{}, fmt.Errorf("%s: invalid launch nonce", source)
	}
	data, err := c.pluginContents(ctx, conn, plugin, "wait-"+nonce+".json", "status", maxContentsStatus)
	if err != nil {
		return WaitStatus{}, err
	}
	var file struct {
		Nonce     string `json:"nonce"`
		UpdatedAt int64  `json:"updated_at"`
		State     string `json:"state"`
		Size      *int64 `json:"size"`
		From      *int64 `json:"from"`
		Log       []byte `json:"log"`
	}
	if json.Unmarshal([]byte(data), &file) != nil || file.Nonce != nonce || file.UpdatedAt <= 0 ||
		(file.State != "starting" && file.State != "ready" && file.State != "stopped") ||
		file.Size == nil || file.From == nil || *file.From < 0 || *file.Size < *file.From ||
		file.Log == nil || len(file.Log) > maxWaitLog || *file.Size-*file.From != int64(len(file.Log)) {
		return WaitStatus{}, fmt.Errorf("%s: invalid status snapshot", source)
	}
	// Redact without shifting the byte offsets shared with terminal reads.
	log := strings.ReplaceAll(string(file.Log), conn.Token, strings.Repeat("*", len(conn.Token)))
	return WaitStatus{Nonce: nonce, UpdatedAt: file.UpdatedAt, State: file.State, Size: *file.Size, From: *file.From, Log: []byte(log)}, nil
}

func (c *Client) pluginContents(ctx context.Context, conn *Connection, plugin, name, kind string, limit int) (string, error) {
	source := "colab: plugin " + kind + " via Jupyter Contents"
	if !contentsPluginID.MatchString(plugin) {
		return "", fmt.Errorf("%s: invalid plugin ID", source)
	}
	if conn == nil || conn.Token == "" {
		return "", fmt.Errorf("%s: runtime proxy credentials unavailable", source)
	}
	base, err := url.Parse(conn.URL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		base.Opaque != "" || base.ForceQuery || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("%s: invalid HTTPS runtime proxy URL", source)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := &http.Client{
		Transport: c.http().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	path := "colaunch/plugins/" + plugin + "/" + name
	absolutePath := "/content/" + path
	endpoint := strings.TrimSuffix(base.String(), "/") + "/api/contents/"
	// Match the Colab VS Code client by escaping the entire absolute path.
	out, missing, err := readContents(ctx, client, conn.Token, endpoint+url.PathEscape(absolutePath), absolutePath, source, kind, limit)
	if !missing {
		return out, err
	}
	// Some runtimes serve /content as the Jupyter root.
	out, _, fallbackErr := readContents(ctx, client, conn.Token, endpoint+path, path, source, kind, limit)
	if fallbackErr != nil {
		return "", fmt.Errorf("%w, fallback: %w", err, fallbackErr)
	}
	return out, nil
}

func readContents(ctx context.Context, client *http.Client, token, requestURL, path, source, kind string, limit int) (out string, missing bool, err error) {
	// Transport errors may include the URL or header values.
	requestError := func(detail string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		return fmt.Errorf("%s: %s", source, detail)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("%s: could not build request", source)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set(proxyTokenHeader, token)
	req.Header.Set(clientAgentHeader, ClientAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", false, requestError("runtime proxy request failed")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", true, fmt.Errorf("%s: %s file unavailable (HTTP 404 at %s)", source, kind, path)
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, fmt.Errorf("%s: HTTP %d (%w)", source, resp.StatusCode, ErrRuntimeProxyAuth)
	default:
		return "", false, fmt.Errorf("%s: HTTP %d", source, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", false, requestError("could not read response")
	}
	if len(data) > limit {
		return "", false, fmt.Errorf("%s: %s response exceeds the size limit", source, kind)
	}
	var file struct {
		Type    string  `json:"type"`
		Path    string  `json:"path"`
		Format  string  `json:"format"`
		Content *string `json:"content"`
	}
	if json.Unmarshal(data, &file) != nil || file.Type != "file" || strings.TrimPrefix(file.Path, "/") != strings.TrimPrefix(path, "/") || file.Content == nil {
		return "", false, fmt.Errorf("%s: response did not contain the requested %s file", source, kind)
	}
	switch file.Format {
	case "text":
		return *file.Content, false, nil
	case "base64":
		b, err := base64.StdEncoding.DecodeString(*file.Content)
		if err != nil {
			return "", false, fmt.Errorf("%s: invalid base64 %s file", source, kind)
		}
		return string(b), false, nil
	default:
		return "", false, fmt.Errorf("%s: unsupported %s file format", source, kind)
	}
}
