package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fake is Cloudflare's API as far as Colaunch uses it, in memory.
type fake struct {
	t  *testing.T
	mu sync.Mutex
	// token is the one it takes; userToken false makes it an account's,
	// which /user/tokens/verify doesn't know.
	token     string
	userToken bool
	// deny refuses, with code 10000, requests whose path has it.
	deny     string
	accounts []Account
	zones    []Zone
	tunnels  map[string]*Tunnel
	ingress  map[string][]ingressRule
	records  map[string]*Record
	policies map[string]map[string]any
	apps     map[string]map[string]any
	// calls counts requests by "METHOD /path-pattern".
	calls map[string]int
	n     int
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{
		t: t, token: "good", userToken: true,
		accounts: []Account{{ID: "acc1", Name: "Ada's account"}},
		zones:    []Zone{{ID: "zone1", Name: "example.com", Status: "active", Account: Account{ID: "acc1"}}},
		tunnels:  map[string]*Tunnel{}, ingress: map[string][]ingressRule{}, records: map[string]*Record{},
		policies: map[string]map[string]any{}, apps: map[string]map[string]any{}, calls: map[string]int{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("good")
	c.Base = srv.URL + "/client/v4"
	return f, c
}

func (f *fake) id(prefix string) string { f.n++; return fmt.Sprintf("%s%d", prefix, f.n) }

func reply(w http.ResponseWriter, status int, result any, errs ...Message) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if errs == nil {
		errs = []Message{}
	}
	json.NewEncoder(w).Encode(map[string]any{"success": status < 300, "errors": errs, "messages": []any{}, "result": result,
		"result_info": map[string]int{"page": 1, "per_page": 50, "total_pages": 1}})
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// The pattern: IDs replaced with "*".
	pat := make([]string, len(parts))
	ids := map[string]bool{"accounts": true, "zones": true, "cfd_tunnel": true, "dns_records": true, "apps": true, "policies": true}
	for i, p := range parts {
		pat[i] = p
		if i > 0 && ids[parts[i-1]] {
			pat[i] = "*"
		}
	}
	key := r.Method + " /" + strings.Join(pat, "/")
	f.calls[key]++
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(w, 401, nil, Message{Code: 1000, Message: "Invalid API Token"})
		return
	}
	if f.deny != "" && strings.Contains(path, f.deny) {
		reply(w, 403, nil, Message{Code: 10000, Message: "Authentication error"})
		return
	}
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	q := r.URL.Query()
	switch {
	case key == "GET /user/tokens/verify":
		if !f.userToken {
			reply(w, 401, nil, Message{Code: 1000, Message: "Invalid API Token"})
			return
		}
		reply(w, 200, map[string]string{"id": "tok", "status": "active"})
	case key == "GET /accounts/*/tokens/verify":
		reply(w, 200, map[string]string{"id": "tok", "status": "active"})
	case key == "GET /accounts":
		reply(w, 200, f.accounts)
	case key == "GET /zones":
		var out []Zone
		for _, z := range f.zones {
			if z.Account.ID == q.Get("account.id") {
				out = append(out, z)
			}
		}
		reply(w, 200, out)
	case key == "GET /accounts/*/cfd_tunnel":
		out := []Tunnel{}
		for _, t := range f.tunnels {
			if (q.Get("name") == "" || t.Name == q.Get("name")) && (q.Get("is_deleted") != "false" || t.DeletedAt == nil) {
				out = append(out, *t)
			}
		}
		reply(w, 200, out)
	case key == "POST /accounts/*/cfd_tunnel":
		if body["config_src"] != "cloudflare" {
			f.t.Errorf("tunnel made with config_src %v", body["config_src"])
		}
		t := &Tunnel{ID: f.id("tun-"), Name: body["name"].(string), ConfigSrc: "cloudflare"}
		f.tunnels[t.ID] = t
		reply(w, 200, t)
	case key == "PUT /accounts/*/cfd_tunnel/*/configurations":
		b, _ := json.Marshal(body["config"].(map[string]any)["ingress"])
		var rules []ingressRule
		json.Unmarshal(b, &rules)
		f.ingress[parts[3]] = rules
		reply(w, 200, map[string]any{"tunnel_id": parts[3]})
	case key == "GET /accounts/*/cfd_tunnel/*/token":
		if f.tunnels[parts[3]] == nil {
			reply(w, 404, nil, Message{Code: 1003, Message: "not found"})
			return
		}
		reply(w, 200, "eyJ-token-"+parts[3])
	case key == "DELETE /accounts/*/cfd_tunnel/*/connections":
		reply(w, 200, nil)
	case key == "DELETE /accounts/*/cfd_tunnel/*":
		delete(f.tunnels, parts[3])
		reply(w, 200, map[string]string{"id": parts[3]})
	case strings.HasPrefix(key, "GET /zones/*/dns_records") || strings.HasPrefix(key, "POST /zones/*/dns_records") ||
		strings.HasPrefix(key, "PATCH /zones/*/dns_records") || strings.HasPrefix(key, "DELETE /zones/*/dns_records"):
		if parts[1] != "zone1" {
			reply(w, 400, nil, Message{Code: 7003, Message: "Could not route to /zones/" + parts[1] + "/dns_records, perhaps your object identifier is invalid?"})
			return
		}
		f.dns(w, r.Method, parts, q.Get("name.exact"), body)
	case key == "GET /accounts/*/access/policies":
		out := []map[string]any{}
		for _, p := range f.policies {
			out = append(out, p)
		}
		reply(w, 200, out)
	case key == "POST /accounts/*/access/policies":
		body["id"] = f.id("pol-")
		f.policies[body["id"].(string)] = body
		reply(w, 200, body)
	case key == "PUT /accounts/*/access/policies/*":
		body["id"] = parts[4]
		f.policies[parts[4]] = body
		reply(w, 200, body)
	case key == "DELETE /accounts/*/access/policies/*":
		delete(f.policies, parts[4])
		reply(w, 200, map[string]string{"id": parts[4]})
	case key == "GET /accounts/*/access/apps":
		out := []map[string]any{}
		for _, a := range f.apps {
			if (q.Get("name") == "" || a["name"] == q.Get("name")) && (q.Get("domain") == "" || a["domain"] == q.Get("domain")) {
				out = append(out, a)
			}
		}
		reply(w, 200, out)
	case key == "POST /accounts/*/access/apps":
		if body["type"] != "self_hosted" {
			f.t.Errorf("app of type %v", body["type"])
		}
		body["id"] = f.id("app-")
		f.apps[body["id"].(string)] = body
		reply(w, 201, body)
	case key == "PUT /accounts/*/access/apps/*":
		body["id"] = parts[4]
		f.apps[parts[4]] = body
		reply(w, 200, body)
	case key == "DELETE /accounts/*/access/apps/*":
		delete(f.apps, parts[4])
		reply(w, 202, map[string]string{"id": parts[4]})
	default:
		f.t.Errorf("unexpected request %s (%s)", key, r.URL)
		reply(w, 404, nil, Message{Code: 7000, Message: "No route for that URI"})
	}
}

func (f *fake) dns(w http.ResponseWriter, method string, parts []string, name string, body map[string]any) {
	switch method {
	case "GET":
		out := []Record{}
		for _, r := range f.records {
			if name == "" || strings.EqualFold(r.Name, name) {
				out = append(out, *r)
			}
		}
		reply(w, 200, out)
	case "POST":
		for _, r := range f.records {
			if strings.EqualFold(r.Name, body["name"].(string)) {
				reply(w, 400, nil, Message{Code: 81053, Message: "An A, AAAA, or CNAME record with that host already exists."})
				return
			}
		}
		if body["proxied"] != true || body["ttl"] != float64(1) {
			f.t.Errorf("record not proxied, or ttl not automatic: %v", body)
		}
		rec := &Record{ID: f.id("rec-"), Type: body["type"].(string), Name: body["name"].(string), Content: body["content"].(string), Proxied: true, Comment: fmt.Sprint(body["comment"])}
		f.records[rec.ID] = rec
		reply(w, 200, rec)
	case "PATCH":
		rec := f.records[parts[3]]
		if c, ok := body["content"].(string); ok {
			rec.Content = c
		}
		if p, ok := body["proxied"].(bool); ok {
			rec.Proxied = p
		}
		reply(w, 200, rec)
	case "DELETE":
		delete(f.records, parts[3])
		reply(w, 200, map[string]string{"id": parts[3]})
	}
}

func target() Target {
	return Target{AccountID: "acc1", ZoneID: "zone1", ZoneName: "example.com", Subdomain: "ai", Name: TunnelName("cfg1"), Port: 8188}
}

// A user's token verifies, and so does an account's, which only the
// account's endpoint knows; a wrong one doesn't.
func TestVerify(t *testing.T) {
	f, c := newFake(t)
	accts, err := c.Verify(context.Background())
	if err != nil || len(accts) != 1 || accts[0].ID != "acc1" {
		t.Fatalf("user token: %v %v", accts, err)
	}
	f.userToken = false
	if _, err := c.Verify(context.Background()); err != nil {
		t.Errorf("account token: %v", err)
	}
	if f.calls["GET /accounts/*/tokens/verify"] != 1 {
		t.Errorf("calls %v", f.calls)
	}
	bad := New("wrong")
	bad.Base = c.Base
	if _, err := bad.Verify(context.Background()); !errors.Is(err, ErrToken) {
		t.Errorf("wrong token: %v", err)
	}
	zones, err := c.Zones(context.Background(), "acc1")
	if err != nil || len(zones) != 1 || zones[0].Name != "example.com" {
		t.Errorf("zones %v %v", zones, err)
	}
	f.deny = "/zones"
	if _, err := c.Zones(context.Background(), "acc1"); !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), PermZone) {
		t.Errorf("zones without the permission: %v", err)
	}
}

// Publishing makes the tunnel, its ingress and a proxied CNAME, and gets
// the token; publishing again makes nothing new.
func TestPublishIdempotent(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	p, err := c.Publish(ctx, target())
	if err != nil {
		t.Fatal(err)
	}
	if p.Hostname != "ai.example.com" || p.TunnelID == "" || p.Token != "eyJ-token-"+p.TunnelID || p.AccessAppID != "" {
		t.Fatalf("published %+v", p)
	}
	rules := f.ingress[p.TunnelID]
	if len(rules) != 2 || rules[0].Hostname != "ai.example.com" || rules[0].Service != "http://127.0.0.1:8188" || rules[1].Service != "http_status:404" || rules[1].Hostname != "" {
		t.Errorf("ingress %+v", rules)
	}
	if len(f.records) != 1 {
		t.Fatalf("records %v", f.records)
	}
	for _, r := range f.records {
		if r.Type != "CNAME" || r.Name != "ai.example.com" || r.Content != p.TunnelID+".cfargotunnel.com" || !r.Proxied {
			t.Errorf("record %+v", r)
		}
	}

	// Again, for another port: the same tunnel and record, the ingress
	// changed.
	t2 := target()
	t2.Port = 8080
	p2, err := c.Publish(ctx, t2)
	if err != nil {
		t.Fatal(err)
	}
	if p2.TunnelID != p.TunnelID || len(f.tunnels) != 1 || len(f.records) != 1 {
		t.Errorf("publishing again made more: %d tunnels, %d records", len(f.tunnels), len(f.records))
	}
	if f.calls["POST /accounts/*/cfd_tunnel"] != 1 || f.calls["POST /zones/*/dns_records"] != 1 {
		t.Errorf("calls %v", f.calls)
	}
	if f.ingress[p.TunnelID][0].Service != "http://127.0.0.1:8080" {
		t.Errorf("ingress not updated: %+v", f.ingress[p.TunnelID])
	}

	// A record of ours turned unproxied is proxied again.
	for _, r := range f.records {
		r.Proxied = false
	}
	if _, err := c.Publish(ctx, t2); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.records {
		if !r.Proxied {
			t.Error("the record stayed unproxied")
		}
	}
	if f.calls["PATCH /zones/*/dns_records/*"] != 1 {
		t.Errorf("calls %v", f.calls)
	}
}

// A record of the name Colaunch didn't make stays as it is, and publishing
// says why it stopped.
func TestPublishConflict(t *testing.T) {
	for _, rec := range []Record{
		{Type: "A", Name: "ai.example.com", Content: "203.0.113.7"},
		{Type: "CNAME", Name: "ai.example.com", Content: "someone-else.example.net"},
		{Type: "CNAME", Name: "ai.example.com", Content: "0000.cfargotunnel.com", Comment: "my own tunnel"},
	} {
		f, c := newFake(t)
		rec.ID = "rec-user"
		r := rec
		f.records[r.ID] = &r
		_, err := c.Publish(context.Background(), target())
		var ce *ConflictError
		if !errors.Is(err, ErrConflict) || !errors.As(err, &ce) || !strings.Contains(err.Error(), rec.Content) {
			t.Errorf("%s %s: %v", rec.Type, rec.Content, err)
		}
		if got := f.records["rec-user"]; got.Content != rec.Content || len(f.records) != 1 || f.calls["PATCH /zones/*/dns_records/*"] != 0 {
			t.Errorf("the user's record was touched: %+v, %d records", got, len(f.records))
		}
	}
	// One that already points at this tunnel is used as it is.
	f, c := newFake(t)
	f.tunnels["tun-x"] = &Tunnel{ID: "tun-x", Name: TunnelName("cfg1")}
	f.records["rec-user"] = &Record{ID: "rec-user", Type: "CNAME", Name: "ai.example.com", Content: "tun-x.cfargotunnel.com", Proxied: true}
	if _, err := c.Publish(context.Background(), target()); err != nil || f.calls["POST /zones/*/dns_records"] != 0 {
		t.Errorf("record to this tunnel: %v, %v", err, f.calls)
	}
}

// Errors say what's wrong: a token without a permission names it, a domain
// of another account says so.
func TestPublishErrors(t *testing.T) {
	f, c := newFake(t)
	f.deny = "/cfd_tunnel"
	_, err := c.Publish(context.Background(), target())
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), PermTunnel) {
		t.Errorf("no tunnel permission: %v", err)
	}
	f.deny = "/dns_records"
	_, err = c.Publish(context.Background(), target())
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), PermDNS) {
		t.Errorf("no DNS permission: %v", err)
	}
	f.deny = ""
	other := target()
	other.ZoneID = "zone-elsewhere"
	if _, err := c.Publish(context.Background(), other); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "not be in this account") {
		t.Errorf("zone of another account: %v", err)
	}
	bad := target()
	bad.Subdomain = "-bad_"
	if _, err := c.Publish(context.Background(), bad); err == nil {
		t.Error("published a bad subdomain")
	}
}

// With emails, the hostname goes behind Access: a policy allowing them and
// an application using it, updated rather than doubled on publishing
// again, and removed once Access is turned off.
func TestPublishAccess(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	tg := target()
	tg.AccessEmails = []string{" Ada@example.com", "bob@example.com", "ada@example.com", ""}
	p, err := c.Publish(ctx, tg)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccessAppID == "" || len(f.apps) != 1 || len(f.policies) != 1 {
		t.Fatalf("access: %+v, %d apps, %d policies", p, len(f.apps), len(f.policies))
	}
	app := f.apps[p.AccessAppID]
	if app["domain"] != "ai.example.com" || app["name"] != "colaunch-cfg1" {
		t.Errorf("app %v", app)
	}
	for _, pol := range f.policies {
		b, _ := json.Marshal(pol["include"])
		if pol["decision"] != "allow" || string(b) != `[{"email":{"email":"ada@example.com"}},{"email":{"email":"bob@example.com"}}]` {
			t.Errorf("policy %v %s", pol, b)
		}
		pb, _ := json.Marshal(app["policies"])
		if !strings.Contains(string(pb), pol["id"].(string)) {
			t.Errorf("app's policies %s", pb)
		}
	}
	tg.AccessEmails = []string{"carol@example.com"}
	p2, err := c.Publish(ctx, tg)
	if err != nil || p2.AccessAppID != p.AccessAppID || len(f.apps) != 1 || len(f.policies) != 1 {
		t.Fatalf("again: %+v %v, %d apps, %d policies", p2, err, len(f.apps), len(f.policies))
	}
	if f.calls["PUT /accounts/*/access/apps/*"] != 1 || f.calls["PUT /accounts/*/access/policies/*"] != 1 {
		t.Errorf("calls %v", f.calls)
	}
	tg.AccessEmails = nil
	if _, err := c.Publish(ctx, tg); err != nil || len(f.apps) != 0 || len(f.policies) != 0 {
		t.Errorf("Access off: %v, %d apps, %d policies", err, len(f.apps), len(f.policies))
	}

	// Another's Access application on the hostname is left alone.
	f.apps["app-user"] = map[string]any{"id": "app-user", "name": "Team app", "domain": "ai.example.com", "type": "self_hosted"}
	tg.AccessEmails = []string{"ada@example.com"}
	if _, err := c.Publish(ctx, tg); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Team app") {
		t.Errorf("another's app: %v", err)
	}
	if len(f.apps) != 1 || f.apps["app-user"]["name"] != "Team app" {
		t.Errorf("apps %v", f.apps)
	}

	// Without the Access permission, publishing with Access says which
	// permission is missing.
	delete(f.apps, "app-user")
	f.deny = "/access/"
	if _, err := c.Publish(ctx, tg); !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), PermAccess) {
		t.Errorf("no Access permission: %v", err)
	}
}

// Removing deletes the tunnel, its records and Access, and leaves records
// Colaunch didn't make; removing again is nothing.
func TestRemove(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	tg := target()
	tg.AccessEmails = []string{"ada@example.com"}
	if _, err := c.Publish(ctx, tg); err != nil {
		t.Fatal(err)
	}
	f.records["rec-user"] = &Record{ID: "rec-user", Type: "TXT", Name: "ai.example.com", Content: "v=keep"}
	if err := c.Remove(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if len(f.tunnels) != 0 || len(f.apps) != 0 || len(f.policies) != 0 || len(f.records) != 1 || f.records["rec-user"] == nil {
		t.Errorf("left %d tunnels, %d apps, %d policies, records %v", len(f.tunnels), len(f.apps), len(f.policies), f.records)
	}
	if f.calls["DELETE /accounts/*/cfd_tunnel/*/connections"] != 1 {
		t.Errorf("calls %v", f.calls)
	}
	if err := c.Remove(ctx, tg); err != nil {
		t.Errorf("removing again: %v", err)
	}
	// Without the Access permission there's no Access to remove.
	f.deny = "/access/"
	if err := c.Remove(ctx, target()); err != nil {
		t.Errorf("without Access: %v", err)
	}
}

func TestValidSubdomain(t *testing.T) {
	for s, want := range map[string]bool{"ai": true, "gpu.lab": true, "a-1": true, "": false, "-a": false, "a_b": false, "a..b": false, "*.x": false} {
		if ValidSubdomain(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}
