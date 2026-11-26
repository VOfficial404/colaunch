package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Target is a port published on a hostname of the user's.
type Target struct {
	AccountID string
	ZoneID    string
	// ZoneName is the domain, as example.com.
	ZoneName string
	// Subdomain goes before it: "ai" for ai.example.com.
	Subdomain string
	// Name names the tunnel, and the Access application and policy:
	// "colaunch-<config ID>". Colaunch takes what has it for its own.
	Name string
	// Port is the runtime's port the hostname leads to, on 127.0.0.1.
	Port int
	// AccessEmails, when there are any, put the hostname behind
	// Cloudflare Access, which lets in only these emails. None removes
	// the Access application Colaunch made for it before.
	AccessEmails []string
}

// Hostname is the target's whole hostname.
func (t Target) Hostname() string {
	return strings.ToLower(strings.Trim(t.Subdomain, ".") + "." + strings.Trim(t.ZoneName, "."))
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidSubdomain reports whether s is one or more DNS labels, as "ai" or
// "gpu.lab".
func ValidSubdomain(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 200 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !label.MatchString(l) {
			return false
		}
	}
	return true
}

// TunnelName is the name of the tunnel, Access application and policy of
// a configuration.
func TunnelName(configID string) string { return "colaunch-" + configID }

// recordComment marks the DNS records Colaunch made.
func recordComment(name string) string { return "Managed by Colaunch (" + name + ")" }

// Published is a target once Cloudflare serves it.
type Published struct {
	TunnelID string
	// Token runs the tunnel: cloudflared tunnel run --token-file.
	Token    string
	Hostname string
	// AccessAppID is the Access application, "" without one.
	AccessAppID string
}

// Tunnel is a cfd_tunnel.
type Tunnel struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ConfigSrc string  `json:"config_src"`
	DeletedAt *string `json:"deleted_at"`
}

// Record is a DNS record.
type Record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// ConflictError is a hostname something Colaunch didn't make uses.
type ConflictError struct {
	Hostname string
	// What is there: "CNAME → other.example.net", or an Access app.
	What string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("Cloudflare already has %s for %s, which Colaunch didn't make: it left it alone. Remove it in Cloudflare's dashboard, or choose another subdomain.", e.What, e.Hostname)
}

// Is matches ErrConflict.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

func tunnelTarget(id string) string { return id + ".cfargotunnel.com" }

// Publish makes, or finds again, everything that serves t, and returns the
// token that runs its tunnel. Running it again changes only what differs.
func (c *Client) Publish(ctx context.Context, t Target) (*Published, error) {
	if t.AccountID == "" || t.ZoneID == "" || t.ZoneName == "" || t.Name == "" {
		return nil, errors.New("cloudflare: choose an account and a domain first")
	}
	if !ValidSubdomain(t.Subdomain) {
		return nil, fmt.Errorf("cloudflare: %q isn't a valid subdomain", t.Subdomain)
	}
	if t.Port <= 0 || t.Port > 65535 {
		return nil, fmt.Errorf("cloudflare: bad port %d", t.Port)
	}
	host := t.Hostname()
	tun, err := c.ensureTunnel(ctx, t.AccountID, t.Name)
	if err != nil {
		return nil, err
	}
	if err := c.putIngress(ctx, t.AccountID, tun.ID, host, t.Port); err != nil {
		return nil, err
	}
	if err := c.ensureCNAME(ctx, t.ZoneID, host, tun.ID, t.Name); err != nil {
		return nil, err
	}
	out := &Published{TunnelID: tun.ID, Hostname: host}
	if len(t.AccessEmails) > 0 {
		if out.AccessAppID, err = c.ensureAccess(ctx, t.AccountID, host, t.Name, t.AccessEmails); err != nil {
			return nil, err
		}
	} else {
		// Access turned off: remove what Colaunch made before. Should
		// that fail, the hostname stays behind Access, which is the safe
		// way to fail; publishing goes on.
		c.removeAccess(ctx, t.AccountID, t.Name)
	}
	if out.Token, err = c.TunnelToken(ctx, t.AccountID, tun.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// findTunnel is the account's tunnel named name, not deleted, nil for none.
//
// GET /accounts/{account_id}/cfd_tunnel?name=…&is_deleted=false, result
// [{id, name, config_src, deleted_at, …}] (API reference: Zero Trust ›
// Tunnels › Cloudflared › List Cloudflare Tunnels).
func (c *Client) findTunnel(ctx context.Context, accountID, name string) (*Tunnel, error) {
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	ts, err := list[Tunnel](ctx, c, "look for the tunnel", PermTunnel, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel", q)
	if err != nil {
		return nil, err
	}
	for i := range ts {
		if ts[i].Name == name && ts[i].DeletedAt == nil {
			return &ts[i], nil
		}
	}
	return nil, nil
}

// ensureTunnel finds the tunnel named name, or makes it, configured from
// Cloudflare.
//
// POST /accounts/{account_id}/cfd_tunnel, body {name, config_src:
// "cloudflare"}, result {id, name, …} (API reference: Zero Trust ›
// Tunnels › Cloudflared › Create a Cloudflare Tunnel). A remotely managed
// tunnel needs no tunnel_secret.
func (c *Client) ensureTunnel(ctx context.Context, accountID, name string) (*Tunnel, error) {
	t, err := c.findTunnel(ctx, accountID, name)
	if err != nil || t != nil {
		return t, err
	}
	var made Tunnel
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if _, err := c.do(ctx, "create the tunnel", PermTunnel, http.MethodPost, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel", nil, body, &made); err != nil {
		return nil, err
	}
	if made.ID == "" {
		return nil, errors.New("cloudflare: the new tunnel has no ID")
	}
	return &made, nil
}

// ingressRule is a rule of a tunnel's ingress; the last has no hostname
// and catches the rest.
type ingressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

// putIngress has the tunnel send host to the runtime's port, and answer
// 404 to anything else.
//
// PUT /accounts/{account_id}/cfd_tunnel/{tunnel_id}/configurations, body
// {config: {ingress: [{hostname, service}, …]}} (API reference: Zero
// Trust › Tunnels › Cloudflared › Configurations › Update Tunnel
// configuration). The catch-all "http_status:404" ends the list, as
// cloudflared's ingress rules require.
func (c *Client) putIngress(ctx context.Context, accountID, tunnelID, host string, port int) error {
	body := map[string]any{"config": map[string]any{"ingress": []ingressRule{
		{Hostname: host, Service: fmt.Sprintf("http://127.0.0.1:%d", port)},
		{Service: "http_status:404"},
	}}}
	_, err := c.do(ctx, "configure the tunnel", PermTunnel, http.MethodPut, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/configurations", nil, body, nil)
	return err
}

// records are the zone's records named host, of any type: an A or AAAA
// record of the name blocks a CNAME as much as another CNAME does.
//
// GET /zones/{zone_id}/dns_records?name.exact=…, result [{id, type, name,
// content, proxied, comment, …}] (API reference: DNS › Records › List DNS
// Records).
func (c *Client) records(ctx context.Context, zoneID, host string) ([]Record, error) {
	q := url.Values{"name.exact": {host}}
	return list[Record](ctx, c, "read the domain's DNS records", PermDNS, "/zones/"+url.PathEscape(zoneID)+"/dns_records", q)
}

// ours reports whether Colaunch made the record, for the tunnel named
// name: a CNAME to a tunnel, marked with its comment.
func ours(r Record, name string) bool {
	return strings.EqualFold(r.Type, "CNAME") && strings.HasSuffix(strings.ToLower(r.Content), ".cfargotunnel.com") && r.Comment == recordComment(name)
}

// ensureCNAME points host at the tunnel, proxied, leaving any record of the
// name that Colaunch didn't make alone.
//
// POST /zones/{zone_id}/dns_records, body {type: "CNAME", name, content,
// proxied: true, ttl: 1, comment} (ttl 1 is "automatic"); PATCH
// /zones/{zone_id}/dns_records/{dns_record_id} with the fields that change
// (API reference: DNS › Records › Create DNS Record, Update DNS Record).
func (c *Client) ensureCNAME(ctx context.Context, zoneID, host, tunnelID, name string) error {
	rs, err := c.records(ctx, zoneID, host)
	if err != nil {
		return err
	}
	want := tunnelTarget(tunnelID)
	var mine *Record
	for i, r := range rs {
		switch {
		case strings.EqualFold(r.Type, "CNAME") && strings.EqualFold(r.Content, want):
			// Pointing at this very tunnel: a record the user made for
			// it is as good.
			mine = &rs[i]
		case ours(r, name):
			mine = &rs[i]
		default:
			if blocks(r.Type) {
				return &ConflictError{Hostname: host, What: fmt.Sprintf("a %s record → %s", r.Type, r.Content)}
			}
		}
	}
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	if mine == nil {
		body := map[string]any{"type": "CNAME", "name": host, "content": want, "proxied": true, "ttl": 1, "comment": recordComment(name)}
		_, err := c.do(ctx, "create the DNS record", PermDNS, http.MethodPost, path, nil, body, nil)
		var api *APIError
		if errors.As(err, &api) && api.kind == nil {
			for _, m := range api.Errors {
				// 81053: "An A, AAAA, or CNAME record with that host
				// already exists"; 81057/81058: "The record already
				// exists" (codes as the API returns them).
				if m.Code == 81053 || m.Code == 81057 || m.Code == 81058 {
					return &ConflictError{Hostname: host, What: "a DNS record"}
				}
			}
		}
		return err
	}
	if strings.EqualFold(mine.Content, want) && mine.Proxied {
		return nil
	}
	body := map[string]any{"type": "CNAME", "content": want, "proxied": true, "ttl": 1}
	_, err = c.do(ctx, "update the DNS record", PermDNS, http.MethodPatch, path+"/"+url.PathEscape(mine.ID), nil, body, nil)
	return err
}

// blocks reports whether a record of this type keeps a CNAME of the same
// name out.
func blocks(typ string) bool {
	switch strings.ToUpper(typ) {
	case "A", "AAAA", "CNAME":
		return true
	}
	// A CNAME can't share its name with any other record either, but
	// Cloudflare refuses that itself, with a clearer message.
	return false
}

// TunnelToken is the token cloudflared runs the tunnel with.
//
// GET /accounts/{account_id}/cfd_tunnel/{tunnel_id}/token, result: the
// token, a string (API reference: Zero Trust › Tunnels › Cloudflared ›
// Token › Get a Cloudflare Tunnel token).
func (c *Client) TunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	var tok string
	if _, err := c.do(ctx, "get the tunnel's token", PermTunnel, http.MethodGet, "/accounts/"+url.PathEscape(accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/token", nil, nil, &tok); err != nil {
		return "", err
	}
	if tok == "" {
		return "", errors.New("cloudflare: the tunnel's token is empty")
	}
	return tok, nil
}

// Remove deletes what Publish made for t: its Access application and
// policy, its DNS records, and its tunnel. Records Colaunch didn't make
// stay. The tunnel must not be running.
func (c *Client) Remove(ctx context.Context, t Target) error {
	if t.AccountID == "" || t.Name == "" {
		return errors.New("cloudflare: choose an account first")
	}
	if err := c.removeAccess(ctx, t.AccountID, t.Name); err != nil {
		return err
	}
	tun, err := c.findTunnel(ctx, t.AccountID, t.Name)
	if err != nil {
		return err
	}
	if t.ZoneID != "" && ValidSubdomain(t.Subdomain) && t.ZoneName != "" {
		rs, err := c.records(ctx, t.ZoneID, t.Hostname())
		if err != nil {
			return err
		}
		for _, r := range rs {
			if !ours(r, t.Name) && !(tun != nil && strings.EqualFold(r.Type, "CNAME") && strings.EqualFold(r.Content, tunnelTarget(tun.ID))) {
				continue
			}
			// DELETE /zones/{zone_id}/dns_records/{dns_record_id} (API
			// reference: DNS › Records › Delete DNS Record).
			if _, err := c.do(ctx, "delete the DNS record", PermDNS, http.MethodDelete, "/zones/"+url.PathEscape(t.ZoneID)+"/dns_records/"+url.PathEscape(r.ID), nil, nil, nil); err != nil {
				return err
			}
		}
	}
	if tun == nil {
		return nil
	}
	base := "/accounts/" + url.PathEscape(t.AccountID) + "/cfd_tunnel/" + url.PathEscape(tun.ID)
	// DELETE …/connections drops connections Cloudflare still tracks
	// after cloudflared stopped; DELETE the tunnel needs none active (API
	// reference: Zero Trust › Tunnels › Cloudflared › Connections › Clean
	// up Cloudflare Tunnel connections; Delete a Cloudflare Tunnel).
	if _, err := c.do(ctx, "clean up the tunnel's connections", PermTunnel, http.MethodDelete, base+"/connections", nil, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if _, err := c.do(ctx, "delete the tunnel", PermTunnel, http.MethodDelete, base, nil, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
