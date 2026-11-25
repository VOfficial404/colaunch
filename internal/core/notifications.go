package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"colaunch/internal/agent"
	"colaunch/internal/colab"
	"colaunch/internal/hf"
	"colaunch/internal/i18n"
	"colaunch/internal/notify"
	"colaunch/internal/plugins"
)

// Notifications are where notices go, and when.
type Notifications struct {
	Channels []notify.Channel `json:"channels,omitempty"`
	// BalanceBelow notices an account's balance going under it, in
	// compute units; 0 for never.
	BalanceBelow float64 `json:"balance_below,omitempty"`
}

// A channel's secret values are kept together in Secrets, as JSON.
func channelSecretKey(id string) string { return "notify/" + id }

// channelsToSave returns the channels as settings.json keeps them, their
// secret values in Secrets.
func (m *Manager) channelsToSave() []notify.Channel {
	var out []notify.Channel
	for _, ch := range m.Settings.Notifications.Channels {
		pub, sec := notify.Split(ch)
		var b []byte
		if len(sec) > 0 {
			b, _ = json.Marshal(sec)
		}
		if err := putSecret(m.Secrets, channelSecretKey(ch.ID), b); err != nil {
			log.Printf("settings: keeping channel %s's secrets: %v", ch.ID, err)
		}
		out = append(out, pub)
	}
	return out
}

// loadChannelSecrets puts the channels' secret values back, and reports
// whether settings.json had any in the open, to write it again without.
func (m *Manager) loadChannelSecrets() (plain bool) {
	for i := range m.Settings.Notifications.Channels {
		ch := &m.Settings.Notifications.Channels[i]
		if ch.ID == "" {
			ch.ID = newID()
		}
		pub, inFile := notify.Split(*ch)
		if len(inFile) > 0 {
			plain = true
		}
		sec := map[string]string{}
		if b, err := m.Secrets.Get(channelSecretKey(ch.ID)); err == nil && len(b) > 0 {
			if err := json.Unmarshal(b, &sec); err != nil {
				log.Printf("settings: channel %s's secrets: %v", ch.ID, err)
			}
		}
		for k, v := range inFile {
			if sec[k] == "" {
				sec[k] = v
			}
		}
		pub.Secret = sec
		*ch = pub
	}
	return plain
}

// Channel returns a channel by its ID, with its secrets.
func (m *Manager) Channel(id string) (notify.Channel, bool) {
	for _, ch := range m.Settings.Notifications.Channels {
		if ch.ID == id {
			return copyChannel(ch), true
		}
	}
	return notify.Channel{}, false
}

func copyChannel(ch notify.Channel) notify.Channel {
	pub, sec := notify.Split(ch)
	pub.Secret = sec
	return pub
}

// SaveChannel adds a channel, or changes the one of its ID, and returns
// it as kept.
func (m *Manager) SaveChannel(ch notify.Channel) notify.Channel {
	ch = copyChannel(ch)
	if ch.ID == "" {
		ch.ID = newID()
	}
	chs := m.Settings.Notifications.Channels
	found := false
	for i := range chs {
		if chs[i].ID == ch.ID {
			chs[i], found = ch, true
		}
	}
	if !found {
		m.Settings.Notifications.Channels = append(chs, ch)
	}
	m.SaveSettings()
	m.pushNotices()
	return ch
}

// DeleteChannel removes a channel, and its secrets.
func (m *Manager) DeleteChannel(id string) {
	chs := m.Settings.Notifications.Channels
	for i := range chs {
		if chs[i].ID == id {
			m.Settings.Notifications.Channels = append(chs[:i:i], chs[i+1:]...)
			break
		}
	}
	m.Secrets.Delete(channelSecretKey(id))
	m.SaveSettings()
	m.pushNotices()
}

// TestChannel sends a test notice through ch, as typed, saved or not;
// done hears how it went, on the main thread.
func (m *Manager) TestChannel(ch notify.Channel, done func(error)) {
	ch = copyChannel(ch)
	msg := notify.Message{Event: notify.EventTest, Title: i18n.T("notice.test_title"), Body: i18n.T("notice.test_body"), Time: time.Now()}
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		err := m.notifier.Send(ctx, ch, msg)
		m.Post(func() { done(err) })
	}()
}

// alert sends a notice to the channels that hear event. byRuntime is a
// stop the runtime's watchdog made, which it told the channels that send
// from the runtime of itself. Call it from the main thread.
func (m *Manager) alert(event, title, body string, byRuntime bool) {
	title, body = m.redact(title), m.redact(body)
	if m.Alert != nil {
		m.Alert(event, title, body)
	}
	msg := notify.Message{Event: event, Title: title, Body: body, Time: time.Now()}
	for _, ch := range m.Settings.Notifications.Channels {
		if event == notify.EventTest || !ch.Wants(event) {
			continue
		}
		if byRuntime && ch.Runtime {
			continue
		}
		ch := copyChannel(ch)
		go func() {
			ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
			defer cancel()
			if err := m.notifier.Send(ctx, ch, msg); err != nil && !errors.Is(err, notify.ErrRepeat) {
				log.Printf("notify: %s (%s): %v", ch.Name, ch.Kind, err)
			}
		}()
	}
}

// watchdogReasons are the reasons the runtime's watchdog stops it for.
var watchdogReasons = map[string]bool{"schedule": true, "idle": true, "budget": true}

// noticeName is the session's model as notices say it: the repository as
// parsed, never the address typed, which may carry a query.
func noticeName(s *Session) string {
	if s.Result != nil && s.Result.Model != "" {
		return s.Result.Model
	}
	if r := s.Values["repo"]; r != "" {
		if ref, err := hf.ParseRef(r); err == nil {
			return ref.Repo
		}
	}
	if p := plugins.Get(s.Plugin); p != nil {
		return p.Name().In(i18n.Lang())
	}
	return s.Plugin
}

func reasonText(reason string) string {
	if k := "reason." + reason; i18n.Has(k) {
		return i18n.T(k)
	}
	return reason
}

func (m *Manager) alertGrabbed(s *Session, got grabbed) {
	m.alert(notify.EventGrabbed, i18n.F("notice.grabbed_title", got.spec.Accelerator),
		i18n.F("notice.grabbed_body", noticeName(s), got.tries, fmtDuration(got.waited)), false)
}

func (m *Manager) alertReady(s *Session) {
	hw := s.Spec.String()
	if s.GPUName != "" {
		hw += " · " + s.GPUName
	}
	m.alert(notify.EventReady, i18n.F("notice.ready_title", noticeName(s)), hw, false)
}

func (m *Manager) alertFailed(s *Session, err error) {
	if errors.Is(err, errGaveUp) {
		m.alert(notify.EventGrabGaveUp, i18n.F("notice.gave_up_title", noticeName(s)), errText(err), false)
		return
	}
	m.alert(notify.EventFailed, i18n.F("notice.failed_title", noticeName(s)), errText(err), false)
}

func (m *Manager) alertStopped(s *Session, byRuntime bool) {
	m.alert(notify.EventStopped, i18n.F("notice.stopped_title", noticeName(s)),
		i18n.F("notice.stopped_body", reasonText(s.StopReason), fmtDuration(s.Uptime())), byRuntime)
}

// checkLowBalance notices an account's balance going under the
// threshold, once each time it does. Main thread.
func (m *Manager) checkLowBalance(a *Account) {
	q := a.Quota
	th := m.Settings.Notifications.BalanceBelow
	if q == nil {
		return
	}
	if q.Balance > 0 {
		a.hadBalance = true
	}
	// A balance of 0 that never was more is an account without compute
	// units, not one that ran low.
	low := th > 0 && q.Balance < th && (q.Balance > 0 || a.hadBalance)
	if low && !a.lowAlerted {
		m.alert(notify.EventBalanceLow, i18n.T("notice.balance_title"), i18n.F("notice.balance_body", a.Email, q.Balance, th), false)
	}
	a.lowAlerted = low
}

func (m *Manager) alertSignedOut(a *Account) {
	m.alert(notify.EventSignedOut, i18n.T("notice.signed_out_title"), i18n.F("notice.signed_out_body", a.Email), false)
}

// runtimeNotices are the requests the session's watchdog makes before it
// stops the runtime, for the channels that send from the runtime. Main
// thread.
func (m *Manager) runtimeNotices(s *Session) agent.Notices {
	n := agent.Notices{Reasons: map[string]string{}}
	for r := range watchdogReasons {
		n.Reasons[r] = reasonText(r)
	}
	msg := notify.Message{
		Event: notify.EventStopped,
		Title: m.redact(i18n.F("notice.stopped_title", noticeName(s))),
		Body:  i18n.F("notice.stopped_runtime", notify.ReasonPlaceholder),
	}
	for _, ch := range m.Settings.Notifications.Channels {
		if !ch.Runtime || !ch.Wants(notify.EventStopped) {
			continue
		}
		r, err := notify.Render(ch, msg)
		if err != nil {
			log.Printf("notify: %s for the runtime: %v", ch.Name, err)
			continue
		}
		n.Requests = append(n.Requests, agent.Notice{Method: r.Method, URL: r.URL, Headers: r.Header, Body: r.Body})
	}
	return n
}

// applyNotices gives the session's runtime the notices to send as it
// stops; a failure only goes to the log.
func (m *Manager) applyNotices(ctx context.Context, s *Session, sh *colab.Shell) {
	var n agent.Notices
	done := make(chan struct{})
	m.peek(func() {
		n = m.runtimeNotices(s)
		close(done)
	})
	select {
	case <-done:
	case <-ctx.Done():
		return
	}
	if err := agent.SetNotices(ctx, sh, n); err != nil {
		m.logf(s, "✗ notices: %s", errText(err))
		return
	}
	if len(n.Requests) > 0 {
		m.logf(s, "The runtime sends %d notice(s) when its watchdog stops it.", len(n.Requests))
	}
}

// pushNotices gives the running sessions' runtimes the notices as the
// settings are now. Main thread.
func (m *Manager) pushNotices() {
	for _, s := range m.Sessions {
		if s.Phase != PhaseRunning {
			continue
		}
		s.mu.Lock()
		sh := s.shell
		s.mu.Unlock()
		if sh == nil {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
			defer cancel()
			m.applyNotices(ctx, s, sh)
		}()
	}
}

var (
	// urlExtras are an address's query and fragment, which carry tokens.
	urlExtras = regexp.MustCompile(`(?i)\b(https?://[^\s?#"'<>]+)[?#][^\s"'<>]*`)
	// urlUser is a user and password in an address.
	urlUser = regexp.MustCompile(`(?i)\b(https?://)[^\s/@"'<>]+@`)
	// tokenLike are tokens by their look: Hugging Face's, Google's access
	// and refresh tokens and client secrets, OpenAI-style keys, bot
	// tokens of Telegram.
	tokenLike = regexp.MustCompile(`\bhf_[A-Za-z0-9]{8,}|\bya29\.[\w.-]+|\b1//[\w-]{10,}|\bGOCSPX-[\w-]+|\bsk-[\w-]{8,}|\b\d{6,}:[\w-]{30,}`)
)

// redact takes out of a notice what may give access: the values of
// secrets the app holds, and what looks like a token or sits in an
// address's query.
func (m *Manager) redact(s string) string {
	s = urlExtras.ReplaceAllString(s, "$1")
	s = urlUser.ReplaceAllString(s, "$1")
	s = tokenLike.ReplaceAllString(s, "***")
	for _, v := range m.secretValues() {
		if len(v) >= 6 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// secretValues are the secrets the app holds in memory.
func (m *Manager) secretValues() []string {
	out := []string{m.HFToken(), m.Settings.OAuth.Secret}
	for plugin, values := range m.Settings.Deploy.Values {
		for _, k := range passwordFields(plugin) {
			out = append(out, values[k])
		}
	}
	for _, s := range m.Sessions {
		for _, k := range passwordFields(s.Plugin) {
			out = append(out, s.Values[k])
		}
		if s.Result != nil {
			out = append(out, s.Result.APIKey)
		}
	}
	for _, ch := range m.Settings.Notifications.Channels {
		for _, v := range ch.Secret {
			out = append(out, v)
		}
	}
	return out
}

// fmtDuration is a duration as notices say it: "1h 20m", "45m", "30s".
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
