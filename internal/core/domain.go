package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/cloudflare"
	"colaunch/internal/colab"
	"colaunch/internal/plugins"
)

// How a domain configuration reaches Cloudflare.
const (
	// DomainToken runs a tunnel the user made in Cloudflare Zero Trust,
	// whose public hostname they point at the port themselves.
	DomainToken = "token"
	// DomainAPI has Colaunch make the tunnel, its DNS record and, if
	// asked, an Access application, with a Cloudflare API token.
	DomainAPI = "api"
)

// DomainConfig is one of the user's own hostnames, served through a named
// Cloudflare Tunnel. Its secrets are kept in Secrets, not settings.json.
type DomainConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Mode is DomainToken or DomainAPI.
	Mode string `json:"mode"`

	// Hostname is what the user pointed at the port, to show: DomainToken.
	Hostname string `json:"hostname,omitempty"`

	// The account, domain and subdomain Colaunch publishes on: DomainAPI.
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	ZoneID      string `json:"zone_id,omitempty"`
	ZoneName    string `json:"zone_name,omitempty"`
	Subdomain   string `json:"subdomain,omitempty"`
	// Access puts the hostname behind Cloudflare Access, which lets in
	// only AccessEmails.
	Access       bool     `json:"access,omitempty"`
	AccessEmails []string `json:"access_emails,omitempty"`

	// TunnelToken (DomainToken) and APIToken (DomainAPI) are kept in
	// Secrets; the fields are there to read files written before, and to
	// carry them in memory.
	TunnelToken string `json:"tunnel_token,omitempty"`
	APIToken    string `json:"api_token,omitempty"`
}

// Host is the configuration's hostname, "" while it's incomplete.
func (d DomainConfig) Host() string {
	if d.Mode == DomainAPI {
		if d.ZoneName == "" || !cloudflare.ValidSubdomain(d.Subdomain) {
			return ""
		}
		return cloudflare.Target{ZoneName: d.ZoneName, Subdomain: d.Subdomain}.Hostname()
	}
	return strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(d.Hostname, "https://"), "http://"), "/")))
}

// Protected reports whether Cloudflare Access guards the hostname.
func (d DomainConfig) Protected() bool {
	return d.Mode == DomainAPI && d.Access && len(cleanEmails(d.AccessEmails)) > 0
}

// Complete reports what the configuration still lacks to publish, nil for
// nothing.
func (d DomainConfig) Complete() error {
	switch d.Mode {
	case DomainToken:
		if strings.TrimSpace(d.TunnelToken) == "" {
			return errors.New("the configuration has no tunnel token")
		}
		if d.Host() == "" {
			return errors.New("the configuration has no hostname")
		}
	case DomainAPI:
		if strings.TrimSpace(d.APIToken) == "" {
			return errors.New("the configuration has no API token")
		}
		if d.AccountID == "" || d.ZoneID == "" {
			return errors.New("choose the configuration's account and domain")
		}
		if d.Host() == "" {
			return errors.New("the configuration's subdomain isn't valid")
		}
		if d.Access && len(cleanEmails(d.AccessEmails)) == 0 {
			return errors.New("Cloudflare Access needs at least one email")
		}
	default:
		return fmt.Errorf("unknown mode %q", d.Mode)
	}
	return nil
}

// target is where Cloudflare's API publishes the configuration, to port.
func (d DomainConfig) target(port int) cloudflare.Target {
	t := cloudflare.Target{AccountID: d.AccountID, ZoneID: d.ZoneID, ZoneName: d.ZoneName, Subdomain: d.Subdomain,
		Name: cloudflare.TunnelName(d.ID), Port: port}
	if d.Access {
		t.AccessEmails = cleanEmails(d.AccessEmails)
	}
	return t
}

func cleanEmails(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// ParseEmails reads emails separated by commas, spaces or lines.
func ParseEmails(s string) []string {
	return cleanEmails(strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}))
}

// domainSecret is what a configuration keeps in Secrets.
type domainSecret struct {
	TunnelToken string `json:"tunnel_token,omitempty"`
	APIToken    string `json:"api_token,omitempty"`
}

func domainSecretKey(id string) string { return "domain/" + id }

// domainsToSave returns the configurations as settings.json keeps them,
// their tokens in Secrets.
func (m *Manager) domainsToSave() []DomainConfig {
	var out []DomainConfig
	for _, d := range m.Settings.Risky.Domains {
		sec := domainSecret{TunnelToken: d.TunnelToken, APIToken: d.APIToken}
		var b []byte
		if sec != (domainSecret{}) {
			b, _ = json.Marshal(sec)
		}
		if err := putSecret(m.Secrets, domainSecretKey(d.ID), b); err != nil {
			log.Printf("settings: keeping domain %s's tokens: %v", d.ID, err)
		}
		d.TunnelToken, d.APIToken = "", ""
		d.AccessEmails = append([]string(nil), d.AccessEmails...)
		out = append(out, d)
	}
	return out
}

// loadDomainSecrets puts the configurations' tokens back, and reports
// whether settings.json had any in the open, to write it again without.
func (m *Manager) loadDomainSecrets() (plain bool) {
	for i := range m.Settings.Risky.Domains {
		d := &m.Settings.Risky.Domains[i]
		if d.ID == "" {
			d.ID = newID()
		}
		if d.TunnelToken != "" || d.APIToken != "" {
			plain = true
		}
		b, err := m.Secrets.Get(domainSecretKey(d.ID))
		if err != nil || len(b) == 0 {
			continue
		}
		var sec domainSecret
		if err := json.Unmarshal(b, &sec); err != nil {
			log.Printf("settings: domain %s's tokens: %v", d.ID, err)
			continue
		}
		if d.TunnelToken == "" {
			d.TunnelToken = sec.TunnelToken
		}
		if d.APIToken == "" {
			d.APIToken = sec.APIToken
		}
	}
	return plain
}

// Domain returns a configuration by its ID, with its tokens.
func (m *Manager) Domain(id string) (DomainConfig, bool) {
	for _, d := range m.Settings.Risky.Domains {
		if d.ID == id {
			d.AccessEmails = append([]string(nil), d.AccessEmails...)
			return d, true
		}
	}
	return DomainConfig{}, false
}

// SaveDomain adds a configuration, or changes the one of its ID, and
// returns it as kept.
func (m *Manager) SaveDomain(d DomainConfig) DomainConfig {
	if d.ID == "" {
		d.ID = newID()
	}
	d.Name = strings.TrimSpace(d.Name)
	d.Subdomain = strings.ToLower(strings.TrimSpace(d.Subdomain))
	d.TunnelToken, d.APIToken = strings.TrimSpace(d.TunnelToken), strings.TrimSpace(d.APIToken)
	d.AccessEmails = cleanEmails(d.AccessEmails)
	ds := m.Settings.Risky.Domains
	found := false
	for i := range ds {
		if ds[i].ID == d.ID {
			ds[i], found = d, true
		}
	}
	if !found {
		m.Settings.Risky.Domains = append(ds, d)
	}
	m.SaveSettings()
	return d
}

// DeleteDomain removes a configuration and its tokens, disconnecting it
// first. What it made on Cloudflare stays: DeleteDomainResources removes
// that.
func (m *Manager) DeleteDomain(id string) {
	if s, l := m.DomainBinding(id); l != nil {
		m.StopDomainLink(s, l.Remote)
	}
	ds := m.Settings.Risky.Domains
	for i := range ds {
		if ds[i].ID == id {
			m.Settings.Risky.Domains = append(ds[:i:i], ds[i+1:]...)
			break
		}
	}
	m.Secrets.Delete(domainSecretKey(id))
	m.SaveSettings()
}

// What domain links refuse.
var (
	// ErrDomainBound is a configuration already serving another port,
	// which has to be disconnected first.
	ErrDomainBound = errors.New("this domain already serves another port: disconnect it first")
	// ErrNoDomain is a configuration that isn't there any more.
	ErrNoDomain = errors.New("no such domain configuration")
)

// DomainLink is a port of a runtime served on one of the user's hostnames
// through a named Cloudflare Tunnel. It's kept nowhere: stopping the
// session, or restarting the app, disconnects it; the tunnel and its DNS
// record stay on Cloudflare for next time.
type DomainLink struct {
	Config string
	Remote int
	// Hostname is where it's served; URL is "https://"+Hostname once
	// cloudflared has connected.
	Hostname  string
	URL       string
	Starting  bool
	Err       string
	Protected bool
	cancel    context.CancelFunc
}

// Live reports whether the link is up, or on its way.
func (l *DomainLink) Live() bool { return l != nil && (l.Starting || l.URL != "") }

// DomainLink is the domain link of a port of the session's runtime, nil
// for none.
func (s *Session) DomainLink(remote int) *DomainLink { return s.Domains[remote] }

// HasDomainLink reports whether any port of the session is on a domain, or
// about to be.
func (s *Session) HasDomainLink() bool {
	for _, l := range s.Domains {
		if l.Live() {
			return true
		}
	}
	return false
}

// Exposed reports whether any port of the session is reachable from the
// internet, through a public link or a domain.
func (s *Session) Exposed() bool { return s.HasPublicLink() || s.HasDomainLink() }

// DomainBinding is the session and link a configuration serves, nils for
// none.
func (m *Manager) DomainBinding(config string) (*Session, *DomainLink) {
	for _, s := range m.Sessions {
		for _, l := range s.Domains {
			if l.Config == config && l.Live() {
				return s, l
			}
		}
	}
	return nil, nil
}

// publishDomain makes what Cloudflare needs to serve d on port, through its
// API, and returns the tunnel's token and hostname; tests replace it.
var publishDomain = func(ctx context.Context, d DomainConfig, port int) (token, host string, err error) {
	p, err := cloudflare.New(d.APIToken).Publish(ctx, d.target(port))
	if err != nil {
		return "", "", err
	}
	return p.Token, p.Hostname, nil
}

// removeDomain deletes what publishDomain made; tests replace it.
var removeDomain = func(ctx context.Context, d DomainConfig) error {
	return cloudflare.New(d.APIToken).Remove(ctx, d.target(0))
}

// domainShell is the session's shell, for the tunnel; tests replace it.
var domainShell = func(m *Manager, ctx context.Context, s *Session, a *Account) (*colab.Shell, error) {
	return m.shellOf(ctx, s, a)
}

// openDomain runs the tunnel on the runtime; tests replace it.
var openDomain = agent.OpenDomain

// stopDomain stops it.
var stopDomain = agent.StopDomain

// StartDomainLink serves a port of a running session on the hostname of the
// configuration config, in the background; s.Domains says how it goes.
// Call it from the main thread.
func (m *Manager) StartDomainLink(s *Session, remote int, config string) error {
	if !m.Settings.Risky.PublicLinks {
		return ErrPublicLinksOff
	}
	if s.Phase != PhaseRunning {
		return errors.New("the runtime isn't running")
	}
	var port *plugins.Port
	for _, f := range s.Forwards {
		if f.Port.Remote == remote {
			p := f.Port
			port = &p
			break
		}
	}
	if port == nil {
		return fmt.Errorf("the runtime serves no port %d", remote)
	}
	if !Shareable(port.Kind) {
		return fmt.Errorf("port %d serves no web page or API", remote)
	}
	if port.Kind == plugins.PortOpenAI && (s.Result == nil || s.Result.APIKey == "") {
		return ErrPublicLinkNeedsKey
	}
	d, ok := m.Domain(config)
	if !ok {
		return ErrNoDomain
	}
	if err := d.Complete(); err != nil {
		return err
	}
	if bs, bl := m.DomainBinding(config); bl != nil {
		if bs == s && bl.Remote == remote {
			return nil
		}
		return ErrDomainBound
	}
	if old := s.Domains[remote]; old.Live() {
		return fmt.Errorf("port %d is already on %s: disconnect it first", remote, old.Hostname)
	}
	a := m.Account(s.Account)
	if a == nil {
		return errors.New("no account")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	l := &DomainLink{Config: config, Remote: remote, Hostname: d.Host(), Starting: true, Protected: d.Protected(), cancel: cancel}
	if s.Domains == nil {
		s.Domains = map[int]*DomainLink{}
	}
	s.Domains[remote] = l
	title := s.Title()
	m.logf(s, "Publishing port %d on %s…", remote, l.Hostname)
	go func() {
		defer cancel()
		token, host := d.TunnelToken, d.Host()
		var err error
		if d.Mode == DomainAPI {
			token, host, err = publishDomain(ctx, d, remote)
		}
		var sh *colab.Shell
		if err == nil {
			sh, err = domainShell(m, ctx, s, a)
		}
		if err == nil {
			err = openDomain(ctx, sh, d.ID, token)
		}
		if ctx.Err() != nil {
			// Disconnected while it connected: make sure nothing is left.
			if sh != nil {
				sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
				stopDomain(sctx, sh, d.ID)
				scancel()
			}
			return
		}
		if err != nil {
			m.logf(s, "✗ Publishing port %d on %s: %s", remote, l.Hostname, errText(err))
		} else {
			m.logf(s, "Port %d is on https://%s — anyone who can reach the hostname can reach the port.", remote, host)
		}
		m.Post(func() {
			if s.Domains[remote] != l {
				return
			}
			l.Starting = false
			if err != nil {
				l.Err = errText(err)
				m.Notify("error", title, l.Err)
				return
			}
			l.Hostname, l.URL = host, "https://"+host
		})
	}()
	return nil
}

// StopDomainLink disconnects a port from its domain: cloudflared stops and
// its token leaves the runtime. The tunnel and DNS record stay on
// Cloudflare. Call it from the main thread.
func (m *Manager) StopDomainLink(s *Session, remote int) {
	l := s.Domains[remote]
	if l == nil {
		return
	}
	delete(s.Domains, remote)
	if l.cancel != nil {
		l.cancel()
	}
	a := m.Account(s.Account)
	if a == nil || s.Ended() {
		return
	}
	m.logf(s, "Disconnecting port %d from %s.", remote, l.Hostname)
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		sh, err := domainShell(m, ctx, s, a)
		if err == nil {
			err = stopDomain(ctx, sh, l.Config)
		}
		if err != nil {
			m.logf(s, "✗ Disconnecting port %d from %s: %s", remote, l.Hostname, errText(err))
		}
	}()
}

// DeleteDomainResources deletes what Colaunch made on Cloudflare for an API
// configuration: its tunnel, DNS record and Access application. done
// hears how it went, on the main thread.
func (m *Manager) DeleteDomainResources(config string, done func(error)) {
	d, ok := m.Domain(config)
	switch {
	case !ok:
		done(ErrNoDomain)
		return
	case d.Mode != DomainAPI:
		done(errors.New("only configurations that use an API token made anything on Cloudflare"))
		return
	case strings.TrimSpace(d.APIToken) == "" || d.AccountID == "":
		done(errors.New("the configuration has no API token or account"))
		return
	}
	if _, l := m.DomainBinding(config); l != nil {
		done(ErrDomainBound)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
		defer cancel()
		err := removeDomain(ctx, d)
		m.Post(func() { done(err) })
	}()
}

// VerifyCloudflare checks an API token and lists the accounts it reaches;
// done hears, on the main thread.
func (m *Manager) VerifyCloudflare(token string, done func([]cloudflare.Account, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		accts, err := cloudflare.New(token).Verify(ctx)
		m.Post(func() { done(accts, err) })
	}()
}

// CloudflareZones lists an account's domains; done hears, on the main
// thread.
func (m *Manager) CloudflareZones(token, account string, done func([]cloudflare.Zone, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		zones, err := cloudflare.New(token).Zones(ctx, account)
		m.Post(func() { done(zones, err) })
	}()
}

// StopAllDomainLinks disconnects every session's domains, as when public
// links are turned off. Call it from the main thread.
func (m *Manager) StopAllDomainLinks() {
	for _, s := range m.Sessions {
		for _, remote := range domainPorts(s) {
			m.StopDomainLink(s, remote)
		}
	}
}

func domainPorts(s *Session) []int {
	var out []int
	for remote := range s.Domains {
		out = append(out, remote)
	}
	sort.Ints(out)
	return out
}

// dropDomainLinks forgets a session's domain links, on the main thread, and
// returns the configurations they ran, for the runtime to stop.
func (s *Session) dropDomainLinks() []string {
	var configs []string
	for _, remote := range domainPorts(s) {
		l := s.Domains[remote]
		if l.cancel != nil {
			l.cancel()
		}
		configs = append(configs, l.Config)
	}
	s.Domains = nil
	return configs
}

// closeDomains stops tunnels on the runtime through the session's shell,
// if it has one; a runtime given back stops them anyway.
func (s *Session) closeDomains(configs []string) {
	if len(configs) == 0 {
		return
	}
	s.mu.Lock()
	sh := s.shell
	s.mu.Unlock()
	if sh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, c := range configs {
		stopDomain(ctx, sh, c)
	}
}
