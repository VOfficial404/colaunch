package app

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/notify"
)

const docsNotifications = "https://github.com/VOfficial404/colaunch/blob/main/docs/notifications.md"

// notificationsState is the Notifications section's state.
type notificationsState struct {
	loaded  bool
	balance float64
	// draft is the channel being added or edited, nil for none; isNew
	// when it's being added.
	draft *notify.Channel
	isNew bool
	// testing is true while the draft's test notice is on its way.
	testing bool
	// confirm asks before deleting toDelete.
	confirm  bool
	toDelete notify.Channel
}

// notifStates holds each App's section state, so that the section needs
// nothing of App's own fields.
var notifStates = map[*App]*notificationsState{}

func (a *App) notifState() *notificationsState {
	s := notifStates[a]
	if s == nil {
		s = &notificationsState{}
		notifStates[a] = s
	}
	return s
}

var iconNotifyBell = lucide(`<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`)
var iconNotifyEdit = lucide(`<path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/><path d="m15 5 4 4"/>`)

// textAccent is the accent for text: the deeper orange on light themes,
// which reads on white.
func textAccent(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return accent
	}
	return accentDeep
}

// notificationsSection is Settings' section of notification channels.
func (a *App) notificationsSection(c *ui.Context) {
	s := a.notifState()
	set := &a.m.Settings.Notifications
	if !s.loaded {
		s.loaded = true
		s.balance = set.BalanceBelow
	}
	t := c.Theme()
	section(c, i18n.T("notify.title"), func() {
		ui.Text(c, i18n.T("notify.help")).FontSize(12).TextColor(t.TextMuted)
		if len(set.Channels) == 0 && s.draft == nil {
			ui.Text(c, i18n.T("notify.none")).FontSize(13).TextColor(t.TextMuted)
		}
		for i := range set.Channels {
			a.channelRow(c, set.Channels[i].ID)
		}
		if s.draft != nil {
			a.channelEditor(c)
		} else {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if labeledButton(c, iconPlus, i18n.T("notify.add"), false).Clicked() {
					s.draft = &notify.Channel{Kind: notify.KindTelegram, Enabled: true, Events: defaultEvents(),
						Params: map[string]string{}, Secret: map[string]string{}}
					s.isNew = true
				}
				if labeledButton(c, iconExternal, i18n.T("notify.docs"), false).Clicked() {
					openURL(docsNotifications)
				}
			})
		}
		ui.Field(c, i18n.T("notify.balance"), func() {
			ui.NumberInput(c, &s.balance, 0, 100000, 1).Width(160)
		})
		ui.Text(c, i18n.T("notify.balance_help")).FontSize(12).TextColor(t.TextMuted)
		a.saveRow(c, s.balance != set.BalanceBelow, func() {
			set.BalanceBelow = s.balance
			a.m.SaveSettings()
		})
	})
	if s.confirm {
		switch dangerConfirm(c, &s.confirm, i18n.T("notify.delete_title"), i18n.F("notify.delete_text", channelName(s.toDelete)), i18n.T("cancel"), i18n.T("notify.delete")) {
		case 1:
			a.m.DeleteChannel(s.toDelete.ID)
			if s.draft != nil && s.draft.ID == s.toDelete.ID {
				s.draft = nil
			}
			s.toDelete = notify.Channel{}
		case 0:
			s.toDelete = notify.Channel{}
		}
	}
}

// defaultEvents are those a new channel hears: all but balance, which
// needs a threshold first.
func defaultEvents() []string {
	return []string{notify.EventGrabbed, notify.EventReady, notify.EventFailed, notify.EventStopped, notify.EventGrabGaveUp, notify.EventSignedOut}
}

func channelName(ch notify.Channel) string {
	if strings.TrimSpace(ch.Name) != "" {
		return ch.Name
	}
	return i18n.T("notify.kind." + ch.Kind)
}

// eventsSummary is a channel's events in a line.
func eventsSummary(ch notify.Channel) string {
	n := 0
	var names []string
	for _, e := range notify.Events {
		if contains(ch.Events, e) {
			n++
			names = append(names, i18n.T("notify.event."+e))
		}
	}
	switch {
	case n == 0:
		return i18n.T("notify.no_events")
	case n == len(notify.Events):
		return i18n.T("notify.all_events")
	case n <= 2:
		return strings.Join(names, ", ")
	}
	return i18n.F("notify.events_summary", n)
}

// channelRow is a channel in the list: its switch, name, type and events,
// and buttons to edit and delete it.
func (a *App) channelRow(c *ui.Context, id string) {
	s := a.notifState()
	set := &a.m.Settings.Notifications
	var ch *notify.Channel
	for i := range set.Channels {
		if set.Channels[i].ID == id {
			ch = &set.Channels[i]
		}
	}
	if ch == nil {
		return
	}
	t := c.Theme()
	name := channelName(*ch)
	ui.Row(c.Key("channel-" + id)).Gap(10).AlignItems(ui.Center).PaddingY(4).Children(func() {
		if ui.Switch(c, &ch.Enabled).Label(i18n.F("notify.toggle", name)).Changed() {
			a.m.SaveChannel(*ch)
		}
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Text(c, name).Bold().SingleLine().Ellipsis("…")
			line := i18n.T("notify.kind."+ch.Kind) + " · " + eventsSummary(*ch)
			ui.Text(c, line).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
		if iconButton(c, iconNotifyEdit, i18n.T("notify.edit")+" "+name).Clicked() {
			if full, ok := a.m.Channel(id); ok {
				s.draft, s.isNew = &full, false
			}
		}
		if iconButton(c, iconTrash, i18n.T("notify.delete")+" "+name).Clicked() {
			s.toDelete, s.confirm = *ch, true
		}
	})
}

// channelEditor adds or edits the draft channel: its type, its values,
// secret ones in password boxes, and its events.
func (a *App) channelEditor(c *ui.Context) {
	s := a.notifState()
	d := s.draft
	t := c.Theme()
	ui.Column(c.Key("channel-editor")).Gap(12).Padding(14).Radius(10).Border(1, t.Border).Children(func() {
		title := i18n.T("notify.edit_title")
		if s.isNew {
			title = i18n.T("notify.new_title")
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconNotifyBell).TextColor(accent)
			ui.Text(c, title).Bold().TextColor(textAccent(c))
		})
		ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
			ui.Field(c, i18n.T("notify.name"), func() {
				ui.TextInput(c, &d.Name).Placeholder(i18n.T("notify.kind." + d.Kind))
			})
			ui.Field(c, i18n.T("notify.kind"), func() {
				labels := make([]string, len(notify.Kinds))
				chosen := ""
				for i, k := range notify.Kinds {
					labels[i] = i18n.T("notify.kind." + k)
					if k == d.Kind {
						chosen = labels[i]
					}
				}
				sel := ui.Select(c, &chosen, labels)
				if !s.isNew {
					sel.Disabled(true)
				}
				if sel.Changed() {
					for i, l := range labels {
						if l == chosen {
							d.Kind = notify.Kinds[i]
						}
					}
				}
			})
		})
		ui.Text(c, i18n.T("notify.help."+d.Kind)).FontSize(12).TextColor(t.TextMuted)
		ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(12).Children(func() {
			for _, f := range notify.Fields(d.Kind) {
				a.channelField(c, d, f)
			}
		})
		ui.Text(c, i18n.T("notify.secret_help")).FontSize(12).TextColor(t.TextMuted)

		ui.Text(c, i18n.T("notify.events")).Bold().FontSize(13)
		ui.Grid(c).Columns(formCols(c)).GapX(16).GapY(6).Children(func() {
			for _, e := range notify.Events {
				on := contains(d.Events, e)
				if ui.Checkbox(c.Key("event-"+e), &on, i18n.T("notify.event."+e)).Changed() {
					if on {
						d.Events = append(d.Events, e)
					} else {
						d.Events = removeString(d.Events, e)
					}
				}
			}
		})
		switchRow(c, &d.Runtime, i18n.T("notify.runtime"))
		ui.Text(c, i18n.T("notify.runtime_help")).FontSize(12).TextColor(t.TextMuted)

		_, err := notify.New(*d, nil)
		if err != nil && touched(*d) {
			ui.Text(c, notifyError(err)).FontSize(12).TextColor(t.Danger)
		}
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			label := i18n.T("notify.test")
			if s.testing {
				label = i18n.T("notify.testing")
			}
			if labeledButton(c, iconSend, label, false).Disabled(err != nil || s.testing).Clicked() {
				s.testing = true
				a.m.TestChannel(*d, func(err error) {
					s.testing = false
					a.testResult(err)
				})
			}
			ui.Spacer(c)
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				s.draft = nil
			}
			if ui.PrimaryButton(c, i18n.T("save")).Disabled(err != nil).Clicked() {
				a.m.SaveChannel(*d)
				s.draft = nil
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("saved"), Type: "success", Timeout: 1500e6})
			}
		})
	})
}

// channelField edits one of the draft's values.
func (a *App) channelField(c *ui.Context, d *notify.Channel, f notify.Field) {
	values := &d.Params
	if f.Secret {
		values = &d.Secret
	}
	if *values == nil {
		*values = map[string]string{}
	}
	v := (*values)[f.Key]
	label := fieldLabel(i18n.T("notify.field."+f.Key), f.Required)
	ui.Field(c.Key(d.Kind+"-"+f.Key), label, func() {
		switch {
		case len(f.Options) > 0:
			labels := make([]string, len(f.Options))
			chosen := ""
			for i, o := range f.Options {
				labels[i] = levelLabel(o)
				if o == v {
					chosen = labels[i]
				}
			}
			if chosen == "" {
				chosen = labels[0]
			}
			if ui.Select(c, &chosen, labels).Changed() {
				for i, l := range labels {
					if l == chosen {
						v = f.Options[i]
					}
				}
			}
		case f.Multiline:
			ui.TextArea(c, &v).Placeholder("Authorization: Bearer …").Height(72)
		default:
			in := ui.TextInput(c, &v).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			if f.Secret {
				in.Password()
			}
			if f.Default != "" {
				in.Placeholder(f.Default)
			}
		}
	})
	if v == "" {
		delete(*values, f.Key)
	} else {
		(*values)[f.Key] = v
	}
}

func levelLabel(level string) string {
	switch level {
	case "":
		return i18n.T("notify.level.default")
	case "timeSensitive":
		return i18n.T("notify.level.time_sensitive")
	}
	return i18n.T("notify.level." + level)
}

// touched reports whether anything was typed in the draft, so that an
// empty one doesn't open on an error.
func touched(ch notify.Channel) bool {
	return len(ch.Secret) > 0 || len(ch.Params) > 0
}

func removeString(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// notifyError says why a notice didn't go, in the user's words.
func notifyError(err error) string {
	var e *notify.Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	field := i18n.T("notify.field." + e.Field)
	var s string
	switch e.Code {
	case "missing", "bad_value", "bad_url", "insecure_url":
		s = i18n.F("notify.err."+e.Code, field)
	case "status":
		s = i18n.F("notify.err.status", e.Status)
	case "kind", "bad_header", "network", "timeout", "rejected":
		s = i18n.T("notify.err." + e.Code)
	default:
		return err.Error()
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// testResult shows how a test notice went.
func (a *App) testResult(err error) {
	if err == nil {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("notify.test_ok"), Type: "success", Timeout: 3e9})
		return
	}
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("notify.test_failed"), Description: notifyError(err), Type: "error", Timeout: 8e9})
}
