package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/cloudflare"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

const docsDomain = docsRisky + "#your-own-domain"

var iconShield = lucide(`<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/><path d="m9 12 2 2 4-4"/>`)

// domainsState is the state of Settings' domains, and of publishing a port
// on one.
type domainsState struct {
	// draft is the configuration being added or edited, nil for none;
	// isNew when it's being added; emails its Access emails as typed.
	draft  *core.DomainConfig
	isNew  bool
	emails string
	// What verifying the draft's API token found.
	verifying bool
	verified  string
	verifyErr string
	accounts  []cloudflare.Account
	zones     []cloudflare.Zone
	zonesFor  string
	loading   bool
	// confirmDelete asks before deleting toDelete; confirmRemove before
	// deleting what toRemove made on Cloudflare, which removing is doing.
	confirmDelete bool
	toDelete      core.DomainConfig
	confirmRemove bool
	toRemove      core.DomainConfig
	removing      string

	// The port being published, bindSession's bindPort, named bindName:
	// its picker, open while bindOpen; the configuration chosen; whether
	// the user says Cloudflare points at the port; and the confirmation.
	bindOpen    bool
	bindSession string
	bindPort    int
	bindName    string
	bindConfig  string
	bindChecked bool
	bindConfirm bool
}

// domainName is a configuration's name, else its hostname.
func domainName(d core.DomainConfig) string {
	if strings.TrimSpace(d.Name) != "" {
		return d.Name
	}
	if h := d.Host(); h != "" {
		return h
	}
	return i18n.T("domain.title")
}

// modeLabel names a configuration's mode.
func modeLabel(mode string) string {
	if mode == core.DomainAPI {
		return i18n.T("domain.mode_api")
	}
	return i18n.T("domain.mode_token")
}

// domainIssue is what the draft lacks to be saved, "" for nothing.
func domainIssue(d core.DomainConfig, emails string) string {
	switch {
	case strings.TrimSpace(d.Name) == "":
		return i18n.T("domain.issue_name")
	case d.Mode == core.DomainToken && d.Host() == "":
		return i18n.T("domain.issue_host")
	case d.Mode == core.DomainToken && strings.TrimSpace(d.TunnelToken) == "":
		return i18n.T("domain.issue_token")
	case d.Mode == core.DomainAPI && strings.TrimSpace(d.APIToken) == "":
		return i18n.T("domain.issue_api_token")
	case d.Mode == core.DomainAPI && (d.AccountID == "" || d.ZoneID == ""):
		return i18n.T("domain.issue_zone")
	case d.Mode == core.DomainAPI && !cloudflare.ValidSubdomain(d.Subdomain):
		return i18n.T("domain.issue_subdomain")
	case d.Mode == core.DomainAPI && d.Access && len(core.ParseEmails(emails)) == 0:
		return i18n.T("domain.issue_emails")
	}
	return ""
}

// domainError is a toast for what Cloudflare refused: a headline in the
// user's language, and what Cloudflare said.
func domainError(err error) ui.Toast {
	title := i18n.T("domain.err_other")
	switch {
	case errors.Is(err, cloudflare.ErrToken):
		title = i18n.T("domain.err_token")
	case errors.Is(err, cloudflare.ErrPermission):
		title = i18n.T("domain.err_permission")
	case errors.Is(err, cloudflare.ErrNotFound):
		title = i18n.T("domain.err_not_found")
	case errors.Is(err, cloudflare.ErrConflict):
		title = i18n.T("domain.err_conflict")
	}
	return ui.Toast{Title: title, Description: err.Error(), Type: "error", Timeout: 10e9}
}

// domainsSection is, under public links in the risky features, the user's
// own domains: their list, and the editor of one.
func (a *App) domainsSection(c *ui.Context) {
	s := &a.domains
	t := c.Theme()
	set := &a.m.Settings.Risky
	ui.Column(c.Key("domains")).Gap(10).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconGlobe).Size(16, 16).TextColor(t.Danger)
			ui.Text(c, i18n.T("domain.title")).Bold().Grow(1).Shrink(1)
		})
		ui.Text(c, i18n.T("domain.help")).FontSize(12).TextColor(t.TextMuted)
		if !set.PublicLinks {
			ui.Text(c, i18n.T("domain.needs_public")).FontSize(12).TextColor(t.Warning)
			return
		}
		if len(set.Domains) == 0 && s.draft == nil {
			ui.Text(c, i18n.T("domain.none")).FontSize(13).TextColor(t.TextMuted)
		}
		for i := range set.Domains {
			a.domainRow(c, set.Domains[i])
		}
		if s.draft != nil {
			a.domainEditor(c)
			return
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if labeledButton(c, iconPlus, i18n.T("domain.add"), false).Clicked() {
				a.editDomain(core.DomainConfig{Mode: core.DomainToken}, true)
			}
			if labeledButton(c, iconExternal, i18n.T("domain.docs"), false).Clicked() {
				openURL(docsDomain)
			}
		})
	})
}

// editDomain opens the editor on d.
func (a *App) editDomain(d core.DomainConfig, isNew bool) {
	s := &a.domains
	s.draft, s.isNew = &d, isNew
	s.emails = strings.Join(d.AccessEmails, ", ")
	s.verified, s.verifyErr, s.verifying = "", "", false
	s.accounts, s.zones, s.zonesFor = nil, nil, ""
	if d.AccountID != "" {
		s.accounts = []cloudflare.Account{{ID: d.AccountID, Name: d.AccountName}}
	}
	if d.ZoneID != "" {
		s.zones, s.zonesFor = []cloudflare.Zone{{ID: d.ZoneID, Name: d.ZoneName}}, d.AccountID
	}
}

// domainRow is a configuration in the list: its name, mode and hostname,
// and buttons to edit and delete it, and to delete what it made on
// Cloudflare.
func (a *App) domainRow(c *ui.Context, d core.DomainConfig) {
	s := &a.domains
	t := c.Theme()
	name := domainName(d)
	ui.Row(c.Key("domain-" + d.ID)).Gap(10).AlignItems(ui.Center).PaddingY(4).Wrap().Children(func() {
		ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(160).Children(func() {
			ui.Text(c, name).Bold().SingleLine().Ellipsis("…")
			parts := []string{modeLabel(d.Mode)}
			if h := d.Host(); h != "" {
				parts = append(parts, h)
			}
			if d.Protected() {
				parts = append(parts, i18n.T("domain.access_on"))
			}
			if _, l := a.m.DomainBinding(d.ID); l != nil {
				parts = append(parts, i18n.T("domain.in_use"))
			}
			ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
		if d.Mode == core.DomainAPI {
			if s.removing == d.ID {
				ui.Spinner(c)
			} else if iconButton(c, iconBroom, i18n.T("domain.remove")+" "+name).Clicked() {
				s.toRemove, s.confirmRemove = d, true
			}
		}
		if iconButton(c, iconNotifyEdit, i18n.T("domain.edit")+" "+name).Clicked() {
			if full, ok := a.m.Domain(d.ID); ok {
				a.editDomain(full, false)
			}
		}
		if iconButton(c, iconTrash, i18n.T("domain.delete")+" "+name).Clicked() {
			s.toDelete, s.confirmDelete = d, true
		}
	})
}

// domainEditor adds or edits the draft configuration.
func (a *App) domainEditor(c *ui.Context) {
	s := &a.domains
	d := s.draft
	t := c.Theme()
	ui.Column(c.Key("domain-editor")).Gap(12).Padding(14).Radius(10).Border(1, t.Border).Children(func() {
		title := i18n.T("domain.edit_title")
		if s.isNew {
			title = i18n.T("domain.new_title")
		}
		ui.Text(c, title).Bold().TextColor(textAccent(c))
		ui.Field(c, i18n.T("domain.name"), func() {
			ui.TextInput(c, &d.Name).Placeholder(i18n.T("domain.name_ph"))
		})
		mode := 0
		if d.Mode == core.DomainAPI {
			mode = 1
		}
		ui.Field(c, i18n.T("domain.mode"), func() {
			if ui.Segmented(c, &mode, i18n.T("domain.mode_token"), i18n.T("domain.mode_api")).Changed() {
				d.Mode = core.DomainToken
				if mode == 1 {
					d.Mode = core.DomainAPI
				}
			}
		})
		if d.Mode == core.DomainAPI {
			a.domainAPIFields(c)
		} else {
			ui.Text(c, i18n.T("domain.token_steps")).FontSize(12).TextColor(t.TextMuted)
			ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
				ui.Field(c, i18n.T("domain.hostname"), func() {
					ui.TextInput(c, &d.Hostname).Placeholder(example("ai.example.com")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
				})
				ui.Field(c, i18n.T("domain.tunnel_token"), func() {
					ui.TextInput(c, &d.TunnelToken).Password().Placeholder(example("eyJhIjoi…"))
				})
			})
		}
		ui.Text(c, i18n.T("domain.secret_help")).FontSize(12).TextColor(t.TextMuted)
		issue := domainIssue(*d, s.emails)
		if issue != "" {
			ui.Text(c, issue).FontSize(12).TextColor(t.Warning)
		}
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				s.draft = nil
			}
			if labeledButton(c, iconCheck, i18n.T("domain.save"), true).Disabled(issue != "").Clicked() {
				d.AccessEmails = core.ParseEmails(s.emails)
				a.m.SaveDomain(*d)
				s.draft = nil
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
			}
		})
	})
}

// domainAPIFields are the editor's fields for an API token: the token and
// its check, the account, domain and subdomain, and Access.
func (a *App) domainAPIFields(c *ui.Context) {
	s := &a.domains
	d := s.draft
	t := c.Theme()
	ui.Text(c, i18n.T("domain.api_perms")).FontSize(12).TextColor(t.TextMuted)
	ui.Field(c, i18n.T("domain.api_token"), func() {
		if ui.TextInput(c, &d.APIToken).Password().Placeholder(example("…")).Changed() {
			s.verified, s.verifyErr = "", ""
		}
	})
	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		label := i18n.T("domain.verify")
		if s.verifying {
			label = i18n.T("domain.verifying")
		}
		if labeledButton(c, iconCheck, label, false).Disabled(s.verifying || strings.TrimSpace(d.APIToken) == "").Clicked() {
			s.verifying, s.verified, s.verifyErr = true, "", ""
			draft := d
			a.m.VerifyCloudflare(d.APIToken, func(accts []cloudflare.Account, err error) {
				s.verifying = false
				if err != nil {
					s.verifyErr = domainError(err).Title + ": " + err.Error()
					return
				}
				s.verified = i18n.F("domain.verified", len(accts))
				s.accounts = accts
				if s.draft == draft && len(accts) > 0 {
					keep := false
					for _, ac := range accts {
						keep = keep || ac.ID == draft.AccountID
					}
					if !keep {
						draft.AccountID, draft.AccountName, draft.ZoneID, draft.ZoneName = accts[0].ID, accts[0].Name, "", ""
					}
					a.loadZones(draft)
				}
			})
		}
		if s.verified != "" {
			ui.Text(c, s.verified).FontSize(12).TextColor(t.Success).Shrink(1)
		}
	})
	if s.verifyErr != "" {
		ui.Text(c, s.verifyErr).FontSize(12).TextColor(t.Danger)
	}
	if len(s.accounts) > 0 {
		ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
			ui.Field(c, i18n.T("domain.account"), func() {
				names := make([]string, len(s.accounts))
				chosen := ""
				for i, ac := range s.accounts {
					names[i] = ac.Name
					if names[i] == "" {
						names[i] = ac.ID
					}
					if ac.ID == d.AccountID {
						chosen = names[i]
					}
				}
				if ui.Select(c, &chosen, names).Changed() {
					for i, n := range names {
						if n == chosen && s.accounts[i].ID != d.AccountID {
							d.AccountID, d.AccountName, d.ZoneID, d.ZoneName = s.accounts[i].ID, s.accounts[i].Name, "", ""
							a.loadZones(d)
						}
					}
				}
			})
			ui.Field(c, i18n.T("domain.zone"), func() {
				switch {
				case s.loading:
					ui.Text(c, i18n.T("domain.loading_zones")).FontSize(13).TextColor(t.TextMuted)
				case len(s.zones) == 0 && s.zonesFor == d.AccountID && d.AccountID != "":
					ui.Text(c, i18n.T("domain.no_zones")).FontSize(12).TextColor(t.Warning)
				default:
					names := make([]string, len(s.zones))
					chosen := ""
					for i, z := range s.zones {
						names[i] = z.Name
						if z.ID == d.ZoneID {
							chosen = z.Name
						}
					}
					if ui.Select(c, &chosen, names).Changed() {
						for _, z := range s.zones {
							if z.Name == chosen {
								d.ZoneID, d.ZoneName = z.ID, z.Name
							}
						}
					}
				}
			})
			ui.Field(c, i18n.T("domain.subdomain"), func() {
				ui.TextInput(c, &d.Subdomain).Placeholder(example("ai")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
		})
		if h := d.Host(); h != "" {
			ui.Text(c, i18n.F("domain.preview", "https://"+h)).Font("monospace").FontSize(12)
		}
	}
	switchRow(c, &d.Access, i18n.T("domain.access"))
	if d.Access {
		ui.Field(c, i18n.T("domain.emails"), func() {
			ui.TextArea(c, &s.emails).Placeholder(i18n.T("domain.emails_ph")).Lines(2, 5)
		})
	}
	ui.Text(c, i18n.T("domain.access_help")).FontSize(12).TextColor(t.TextMuted)
}

// loadZones lists the domains of d's account, choosing the first if d has
// none.
func (a *App) loadZones(d *core.DomainConfig) {
	s := &a.domains
	if d.APIToken == "" || d.AccountID == "" {
		return
	}
	s.loading, s.zones = true, nil
	account := d.AccountID
	a.m.CloudflareZones(d.APIToken, account, func(zones []cloudflare.Zone, err error) {
		if d.AccountID != account {
			return
		}
		s.loading, s.zonesFor = false, account
		if err != nil {
			s.verifyErr = domainError(err).Title + ": " + err.Error()
			return
		}
		s.zones = zones
		if d.ZoneID == "" && len(zones) > 0 {
			d.ZoneID, d.ZoneName = zones[0].ID, zones[0].Name
		}
	})
}

// domainsDialogs ask before deleting a configuration, or what it made on
// Cloudflare.
func (a *App) domainsDialogs(c *ui.Context) {
	s := &a.domains
	if s.confirmDelete {
		switch dangerConfirm(c, &s.confirmDelete, i18n.T("domain.delete_title"), i18n.F("domain.delete_text", domainName(s.toDelete)), i18n.T("cancel"), i18n.T("domain.delete")) {
		case 1:
			a.m.DeleteDomain(s.toDelete.ID)
			if s.draft != nil && s.draft.ID == s.toDelete.ID {
				s.draft = nil
			}
			s.toDelete = core.DomainConfig{}
		case 0:
			s.toDelete = core.DomainConfig{}
		}
	}
	if s.confirmRemove {
		host := s.toRemove.Host()
		if host == "" {
			host = domainName(s.toRemove)
		}
		switch dangerConfirm(c, &s.confirmRemove, i18n.T("domain.remove_title"), i18n.F("domain.remove_text", host), i18n.T("cancel"), i18n.T("domain.remove_confirm")) {
		case 1:
			id := s.toRemove.ID
			s.removing = id
			a.m.DeleteDomainResources(id, func(err error) {
				if s.removing == id {
					s.removing = ""
				}
				if err != nil {
					a.toasts = append(a.toasts, domainError(err))
					return
				}
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("domain.removed"), Type: "success", Timeout: 3e9})
			})
			s.toRemove = core.DomainConfig{}
		case 0:
			s.toRemove = core.DomainConfig{}
		}
	}
}

// exposedWithoutAccess reports whether publishing a port of this kind on d
// lets anyone use the GPU: a web page or ComfyUI, without Access.
func exposedWithoutAccess(k plugins.PortKind, d core.DomainConfig) bool {
	return (k == plugins.PortWeb || k == plugins.PortComfy) && !d.Protected()
}

// domainLink shows, under a forwarded port, the domain it's published on,
// or what publishes it on one.
func (a *App) domainLink(c *ui.Context, s *core.Session, f *core.ForwardState) {
	t := c.Theme()
	r := &a.domains
	if !core.Shareable(f.Port.Kind) {
		return
	}
	l := s.DomainLink(f.Port.Remote)
	set := &a.m.Settings.Risky
	if l == nil && (!set.PublicLinks || len(set.Domains) == 0) {
		return
	}
	ui.Column(c.Key("domain")).Gap(6).Children(func() {
		switch {
		case l != nil && l.URL != "":
			ui.Column(c).Gap(6).Padding(8, 10).Radius(8).Border(1, t.Danger).Children(func() {
				if l.Protected {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, iconShield).Size(16, 16).TextColor(t.Warning)
						ui.Text(c, i18n.T("domain.protected")).FontSize(12).Bold().TextColor(t.Warning).Shrink(1)
					})
				} else {
					riskyWarning(c, i18n.T("share.public"))
				}
				a.copyable(c, i18n.T("domain.link"), l.URL)
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if labeledButton(c, iconExternal, i18n.T("open"), false).Clicked() {
						openURL(l.URL)
					}
					if labeledButton(c, iconStop, i18n.T("domain.disconnect"), false).TextColor(t.Danger).Clicked() {
						a.m.StopDomainLink(s, f.Port.Remote)
					}
				})
			})
		case l != nil && l.Starting:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, i18n.F("domain.starting", l.Hostname)).FontSize(13).Grow(1).Shrink(1)
				if ui.Button(c, i18n.T("cancel")).Clicked() {
					a.m.StopDomainLink(s, f.Port.Remote)
				}
			})
		default:
			if l != nil && l.Err != "" {
				ui.Text(c, l.Err).FontSize(12).TextColor(t.Danger)
			}
			if !set.PublicLinks || len(set.Domains) == 0 {
				return
			}
			noKey := f.Port.Kind == plugins.PortOpenAI && (s.Result == nil || s.Result.APIKey == "")
			if r.bindOpen && r.bindSession == s.ID && r.bindPort == f.Port.Remote && !noKey {
				a.domainPicker(c, s, f)
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				if labeledButton(c, iconGlobe, i18n.T("domain.publish"), false).TextColor(t.Danger).Disabled(noKey).Clicked() {
					r.bindOpen, r.bindSession, r.bindPort, r.bindName, r.bindChecked = true, s.ID, f.Port.Remote, f.Port.Name, false
					if _, ok := a.m.Domain(r.bindConfig); !ok {
						r.bindConfig = set.Domains[0].ID
					}
				}
				if noKey {
					ui.Text(c, i18n.T("share.needs_key")).FontSize(12).TextColor(t.Warning).Shrink(1)
				}
			})
		}
	})
}

// domainPicker chooses the domain a port goes on, says what Cloudflare must
// have, and warns before it's published.
func (a *App) domainPicker(c *ui.Context, s *core.Session, f *core.ForwardState) {
	t := c.Theme()
	r := &a.domains
	d, _ := a.m.Domain(r.bindConfig)
	ui.Column(c.Key("domain-picker")).Gap(8).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
		ui.Field(c, i18n.T("domain.choose"), func() {
			names := make([]string, len(a.m.Settings.Risky.Domains))
			chosen := ""
			for i, x := range a.m.Settings.Risky.Domains {
				names[i] = domainName(x)
				if h := x.Host(); h != "" && h != names[i] {
					names[i] += " (" + h + ")"
				}
				if x.ID == d.ID {
					chosen = names[i]
				}
			}
			if ui.Select(c, &chosen, names).Changed() {
				for i, n := range names {
					if n == chosen {
						r.bindConfig, r.bindChecked = a.m.Settings.Risky.Domains[i].ID, false
					}
				}
			}
		})
		host := d.Host()
		service := fmt.Sprintf("http://localhost:%d", f.Port.Remote)
		if d.Mode == core.DomainToken {
			ui.Text(c, i18n.F("domain.target", host, service)).FontSize(12).TextColor(t.TextMuted)
			ui.Checkbox(c.Key("domain-done"), &r.bindChecked, i18n.F("domain.target_done", host, service))
		}
		if bs, bl := a.m.DomainBinding(d.ID); bl != nil && !(bs == s && bl.Remote == f.Port.Remote) {
			ui.Text(c, i18n.F("domain.moves", domainName(d), fmt.Sprintf("%s :%d", shortTitle(bs), bl.Remote))).FontSize(12).TextColor(t.Warning)
		}
		if exposedWithoutAccess(f.Port.Kind, d) {
			riskyWarning(c, i18n.T("domain.gpu_warning"))
		}
		ready := d.ID != "" && d.Complete() == nil && (d.Mode != core.DomainToken || r.bindChecked)
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				r.bindOpen = false
			}
			if labeledButton(c, iconGlobe, i18n.T("domain.publish_go"), false).TextColor(t.Danger).Disabled(!ready).Clicked() {
				r.bindOpen, r.bindConfirm = false, true
			}
		})
	})
}

// domainDialog asks, each time, before a port goes on a domain, and
// disconnects the domain from where it served before.
func (a *App) domainDialog(c *ui.Context) {
	r := &a.domains
	if !r.bindConfirm {
		return
	}
	d, _ := a.m.Domain(r.bindConfig)
	s := a.m.Session(r.bindSession)
	text := i18n.T("domain.confirm_text")
	if s != nil {
		for _, f := range s.Forwards {
			if f.Port.Remote == r.bindPort && exposedWithoutAccess(f.Port.Kind, d) {
				text += "\n\n" + i18n.T("domain.gpu_warning")
			}
		}
	}
	if bs, bl := a.m.DomainBinding(d.ID); bl != nil && !(bs == s && bl.Remote == r.bindPort) {
		text += "\n\n" + i18n.F("domain.moves", domainName(d), fmt.Sprintf("%s :%d", shortTitle(bs), bl.Remote))
	}
	switch dangerConfirm(c, &r.bindConfirm, i18n.F("domain.confirm_title", r.bindName, d.Host()), text, i18n.T("cancel"), i18n.T("domain.confirm")) {
	case 1:
		if s != nil {
			if bs, bl := a.m.DomainBinding(d.ID); bl != nil && !(bs == s && bl.Remote == r.bindPort) {
				a.m.StopDomainLink(bs, bl.Remote)
			}
			if err := a.m.StartDomainLink(s, r.bindPort, d.ID); err != nil {
				a.toasts = append(a.toasts, ui.Toast{Title: err.Error(), Type: "error", Timeout: 5e9})
			}
		}
		r.bindSession = ""
	case 0:
		r.bindSession = ""
	}
}
