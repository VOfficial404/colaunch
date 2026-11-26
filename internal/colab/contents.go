package colab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Match the plugin manifest and runtime agent's directory naming rules.
var logPluginID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)

// The Contents API returns a whole JSON file model, with no tail option.
const maxContentsLog = 4 << 20

// PluginLog reads only the plugin's log through the runtime's Contents API.
// The VS Code client confirms the proxy route and headers. Whether a live
// runtime exposes /content as the Jupyter root is unverified, so try only
// the relative path and leave logs unavailable if it isn't there.
func (c *Client) PluginLog(ctx context.Context, conn *Connection, plugin string) (string, error) {
	const source = "colab: plugin log via Jupyter Contents"
	if !logPluginID.MatchString(plugin) {
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
	// Transport errors may include the URL or header values.
	requestError := func(detail string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		return fmt.Errorf("%s: %s", source, detail)
	}
	path := "colaunch/plugins/" + plugin + "/log.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base.String(), "/")+"/api/contents/"+path, nil)
	if err != nil {
		return "", fmt.Errorf("%s: could not build request", source)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(proxyTokenHeader, conn.Token)
	req.Header.Set(clientAgentHeader, ClientAgent)
	client := &http.Client{
		Transport: c.http().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", requestError("runtime proxy request failed")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("%s: relative log file unavailable (HTTP 404)", source)
	default:
		return "", fmt.Errorf("%s: HTTP %d", source, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxContentsLog+1))
	if err != nil {
		return "", requestError("could not read response")
	}
	if len(data) > maxContentsLog {
		return "", fmt.Errorf("%s: log response exceeds the size limit", source)
	}
	var file struct {
		Type    string  `json:"type"`
		Path    string  `json:"path"`
		Format  string  `json:"format"`
		Content *string `json:"content"`
	}
	if json.Unmarshal(data, &file) != nil || file.Type != "file" || strings.TrimPrefix(file.Path, "/") != path || file.Content == nil {
		return "", fmt.Errorf("%s: response did not contain the requested log file", source)
	}
	log := *file.Content
	switch file.Format {
	case "text":
	case "base64":
		b, err := base64.StdEncoding.DecodeString(log)
		if err != nil {
			return "", fmt.Errorf("%s: invalid base64 log file", source)
		}
		log = string(b)
	default:
		return "", fmt.Errorf("%s: unsupported log file format", source)
	}
	// Keep the credential out of diagnostics even if the proxy echoes it.
	return strings.ReplaceAll(log, conn.Token, "[redacted]"), nil
}
