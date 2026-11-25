// Package notify sends Colaunch's notices to the services people read them
// on: Telegram, Bark, Server酱, PushPlus, ntfy, Discord and any webhook.
//
// A Channel is one of them as the user set it up. Render turns a message
// into the HTTP request a channel takes, which Send makes, once more if it
// fails, and which the app can also hand to the runtime to make with the
// app closed. Every body is JSON, so that the runtime can put the reason
// in with a plain substitution.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Events a channel can subscribe to.
const (
	EventGrabbed    = "grabbed"
	EventReady      = "ready"
	EventFailed     = "failed"
	EventStopped    = "stopped"
	EventGrabGaveUp = "grab_gave_up"
	EventBalanceLow = "balance_low"
	EventSignedOut  = "signed_out"
	// EventTest is the test notice, which no channel subscribes to and
	// which isn't held back as a repeat.
	EventTest = "test"
)

// Events are the events channels subscribe to, in the order they show.
var Events = []string{EventGrabbed, EventReady, EventFailed, EventStopped, EventGrabGaveUp, EventBalanceLow, EventSignedOut}

// Kinds of channel.
const (
	KindTelegram   = "telegram"
	KindBark       = "bark"
	KindServerChan = "serverchan"
	KindPushPlus   = "pushplus"
	KindNtfy       = "ntfy"
	KindDiscord    = "discord"
	KindWebhook    = "webhook"
)

// Kinds are the kinds of channel, in the order they show.
var Kinds = []string{KindTelegram, KindBark, KindServerChan, KindPushPlus, KindNtfy, KindDiscord, KindWebhook}

// The services' own addresses.
const (
	DefaultTelegramAPI = "https://api.telegram.org"
	DefaultBark        = "https://api.day.app"
	DefaultNtfy        = "https://ntfy.sh"
	ServerChanAPI      = "https://sctapi.ftqq.com"
	PushPlusAPI        = "https://www.pushplus.plus/send"
)

// ReasonPlaceholder and TimePlaceholder stand, in a body rendered for the
// runtime, for why it stopped and when.
const (
	ReasonPlaceholder = "{reason}"
	TimePlaceholder   = "{time}"
)

// Field is a value a kind of channel takes.
type Field struct {
	Key string
	// Secret values are kept in the system's keychain, and typed in
	// password boxes.
	Secret   bool
	Required bool
	// Default is used when the value is empty.
	Default string
	// Options, when there are any, are the values to choose from, "" the
	// service's default.
	Options []string
	// Multiline takes several lines, as the webhook's headers.
	Multiline bool
	// URL is an address, checked with CheckURL.
	URL bool
}

// Fields are the values a kind of channel takes, in the order they show.
func Fields(kind string) []Field {
	switch kind {
	case KindTelegram:
		return []Field{
			{Key: "bot_token", Secret: true, Required: true},
			{Key: "chat_id", Required: true},
			{Key: "api_base", Default: DefaultTelegramAPI, URL: true},
		}
	case KindBark:
		return []Field{
			{Key: "server", Default: DefaultBark, URL: true},
			{Key: "device_key", Secret: true, Required: true},
			{Key: "group", Default: "Colaunch"},
			{Key: "sound"},
			{Key: "level", Options: []string{"", "active", "timeSensitive", "passive", "critical"}},
		}
	case KindServerChan:
		return []Field{{Key: "sendkey", Secret: true, Required: true}}
	case KindPushPlus:
		return []Field{{Key: "token", Secret: true, Required: true}}
	case KindNtfy:
		return []Field{
			{Key: "server", Default: DefaultNtfy, URL: true},
			// Anyone who knows a topic on a public server reads it.
			{Key: "topic", Secret: true, Required: true},
			{Key: "token", Secret: true},
		}
	case KindDiscord:
		return []Field{{Key: "url", Secret: true, Required: true, URL: true}}
	case KindWebhook:
		return []Field{
			{Key: "url", Secret: true, Required: true, URL: true},
			{Key: "headers", Secret: true, Multiline: true},
		}
	}
	return nil
}

// Channel is a service notices go to, as the user set it up.
type Channel struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Events are those it hears.
	Events []string `json:"events"`
	// Params are its values that aren't secret.
	Params map[string]string `json:"params,omitempty"`
	// Runtime has the runtime send the notice of its stopping, with the
	// app closed: the request, with the channel's secrets, goes to a
	// folder of the runtime's only its owner reads.
	Runtime bool `json:"runtime,omitempty"`
	// Secret are its secret values. They are kept in the system's
	// keychain, not in settings.json; the field is there to read files
	// written before.
	Secret map[string]string `json:"secret,omitempty"`
}

// Wants reports whether the channel hears event.
func (c Channel) Wants(event string) bool {
	if event == EventTest {
		return true
	}
	if !c.Enabled {
		return false
	}
	for _, e := range c.Events {
		if e == event {
			return true
		}
	}
	return false
}

// Value is a value of the channel, its field's default when it's empty.
func (c Channel) Value(key string) string {
	for _, f := range Fields(c.Kind) {
		if f.Key != key {
			continue
		}
		var v string
		if f.Secret {
			v = c.Secret[key]
		} else {
			v = c.Params[key]
		}
		if v = strings.TrimSpace(v); v == "" {
			return f.Default
		}
		return v
	}
	return ""
}

// Split returns a copy of the channel without its secret values, and
// those values, wherever in it they were.
func Split(c Channel) (public Channel, secret map[string]string) {
	public = c
	public.Secret = nil
	public.Events = append([]string(nil), c.Events...)
	public.Params = map[string]string{}
	secret = map[string]string{}
	for k, v := range c.Params {
		public.Params[k] = v
	}
	for _, f := range Fields(c.Kind) {
		if !f.Secret {
			continue
		}
		v := c.Secret[f.Key]
		if v == "" {
			v = c.Params[f.Key]
		}
		delete(public.Params, f.Key)
		if v != "" {
			secret[f.Key] = v
		}
	}
	if len(public.Params) == 0 {
		public.Params = nil
	}
	return public, secret
}

// Message is a notice.
type Message struct {
	Event string
	Title string
	Body  string
	// Time is when it happened; zero, in a request for the runtime,
	// leaves TimePlaceholder.
	Time time.Time
}

// Error is why a notice didn't go, in words without the channel's
// secrets.
type Error struct {
	// Code is "kind", "missing", "bad_value", "bad_url", "insecure_url",
	// "bad_header", "network", "timeout", "status" or "rejected".
	Code string
	// Field is the value at fault, for "missing", "bad_value", "bad_url",
	// "insecure_url" and "bad_header".
	Field string
	// Status is the HTTP status, for "status".
	Status int
	// Detail is what the service or the network said.
	Detail string
}

func (e *Error) Error() string {
	var s string
	switch e.Code {
	case "kind":
		s = "unknown kind of channel"
	case "missing":
		s = e.Field + " is required"
	case "bad_value":
		s = e.Field + " isn't valid"
	case "bad_url":
		s = e.Field + " isn't a valid http(s) address"
	case "insecure_url":
		s = e.Field + " must be https (http only on this machine or a private network)"
	case "bad_header":
		s = "headers must be lines of Name: value"
	case "network":
		s = "couldn't reach the service"
	case "timeout":
		s = "the service didn't answer in time"
	case "status":
		s = fmt.Sprintf("the service answered %d", e.Status)
	case "rejected":
		s = "the service refused the notice"
	default:
		s = e.Code
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return "notify: " + s
}

// Configuration reports whether the error is in the channel's values, so
// that trying again can't help.
func (e *Error) Configuration() bool {
	switch e.Code {
	case "kind", "missing", "bad_value", "bad_url", "insecure_url", "bad_header":
		return true
	}
	return false
}

// CheckURL checks an address notices go to: https, or http to this
// machine or a private network (10/8, 172.16/12, 192.168/16, fc00::/7),
// as a Bark or ntfy server of one's own; no user and password in it.
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, &Error{Code: "bad_url"}
	}
	if u.User != nil {
		return nil, &Error{Code: "bad_url", Detail: "no user or password in the address"}
	}
	if u.Scheme == "http" && !localHost(u.Hostname()) {
		return nil, &Error{Code: "insecure_url"}
	}
	return u, nil
}

// localHost reports whether host is this machine or on a private network.
// Only addresses and "localhost" count: a name could resolve anywhere.
func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate()
}

// Request is an HTTP request a notice goes as.
type Request struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Header map[string]string `json:"headers"`
	Body   string            `json:"body"`
	// check reads the service's answer.
	check func(status int, body []byte) error
}

var (
	telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	plainKey      = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	sctpKey       = regexp.MustCompile(`^sctp(\d+)t`)
	ntfyTopic     = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	headerName    = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// Render makes the request that sends m through ch.
func Render(ch Channel, m Message) (*Request, error) {
	fields := Fields(ch.Kind)
	if fields == nil {
		return nil, &Error{Code: "kind"}
	}
	for _, f := range fields {
		if f.Required && ch.Value(f.Key) == "" {
			return nil, &Error{Code: "missing", Field: f.Key}
		}
		if f.URL && ch.Value(f.Key) != "" {
			if _, err := CheckURL(ch.Value(f.Key)); err != nil {
				e := err.(*Error)
				e.Field = f.Key
				return nil, e
			}
		}
	}
	r := &Request{Method: http.MethodPost, Header: map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"User-Agent":   "Colaunch",
	}}
	var body any
	switch ch.Kind {
	case KindTelegram:
		token := ch.Value("bot_token")
		if !telegramToken.MatchString(token) {
			return nil, &Error{Code: "bad_value", Field: "bot_token"}
		}
		r.URL = strings.TrimRight(ch.Value("api_base"), "/") + "/bot" + token + "/sendMessage"
		body = map[string]any{"chat_id": ch.Value("chat_id"), "text": joinText(m.Title, m.Body)}
		r.check = checkTelegram
	case KindBark:
		key := ch.Value("device_key")
		if !plainKey.MatchString(key) {
			return nil, &Error{Code: "bad_value", Field: "device_key"}
		}
		r.URL = strings.TrimRight(ch.Value("server"), "/") + "/push"
		b := map[string]any{"device_key": key, "title": m.Title, "body": m.Body, "group": ch.Value("group")}
		if s := ch.Value("sound"); s != "" {
			b["sound"] = s
		}
		if l := ch.Value("level"); l != "" {
			b["level"] = l
		}
		body = b
		r.check = checkCode("code", 200)
	case KindServerChan:
		key := ch.Value("sendkey")
		if !plainKey.MatchString(key) {
			return nil, &Error{Code: "bad_value", Field: "sendkey"}
		}
		// Server酱³'s keys, sctp<uid>t…, go to the user's own address.
		if sm := sctpKey.FindStringSubmatch(key); sm != nil {
			r.URL = "https://" + sm[1] + ".push.ft07.com/send/" + key + ".send"
		} else {
			r.URL = ServerChanAPI + "/" + key + ".send"
		}
		// Its title is a line of at most 32 characters.
		body = map[string]any{"title": truncate(oneLine(m.Title), 32), "desp": m.Body}
		r.check = checkCode("code", 0)
	case KindPushPlus:
		r.URL = PushPlusAPI
		body = map[string]any{"token": ch.Value("token"), "title": m.Title, "content": m.Body, "template": "txt"}
		r.check = checkCode("code", 200)
	case KindNtfy:
		topic := ch.Value("topic")
		if !ntfyTopic.MatchString(topic) {
			return nil, &Error{Code: "bad_value", Field: "topic"}
		}
		// Published as JSON, to the server's root: titles in Chinese
		// don't fit in headers.
		r.URL = strings.TrimRight(ch.Value("server"), "/") + "/"
		body = map[string]any{"topic": topic, "title": m.Title, "message": m.Body, "tags": []string{"colaunch"}}
		if t := ch.Value("token"); t != "" {
			r.Header["Authorization"] = "Bearer " + t
		}
		r.check = checkOK
	case KindDiscord:
		r.URL = ch.Value("url")
		body = map[string]any{
			"content": truncate("**"+m.Title+"**\n"+m.Body, 2000),
			// Names in a model's name don't ping anyone.
			"allowed_mentions": map[string]any{"parse": []string{}},
		}
		r.check = checkOK
	case KindWebhook:
		r.URL = ch.Value("url")
		headers, err := ParseHeaders(ch.Value("headers"))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			r.Header[k] = v
		}
		when := TimePlaceholder
		if !m.Time.IsZero() {
			when = m.Time.UTC().Format(time.RFC3339)
		}
		body = map[string]any{"event": m.Event, "title": m.Title, "body": m.Body, "time": when}
		r.check = checkOK
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	r.Body = strings.TrimSuffix(buf.String(), "\n")
	return r, nil
}

// ParseHeaders reads headers as lines of "Name: value".
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !headerName.MatchString(k) || strings.ContainsAny(v, "\r\n") {
			return nil, &Error{Code: "bad_header", Field: "headers"}
		}
		switch http.CanonicalHeaderKey(k) {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection":
			return nil, &Error{Code: "bad_header", Field: "headers"}
		}
		out[http.CanonicalHeaderKey(k)] = v
	}
	return out, nil
}

func joinText(title, body string) string {
	if body == "" {
		return title
	}
	return title + "\n" + body
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// serviceMessage is what a service's JSON answer says went wrong.
func serviceMessage(body []byte) string {
	var v map[string]any
	if json.Unmarshal(body, &v) == nil {
		for _, k := range []string{"description", "message", "msg", "error", "info"} {
			if s, ok := v[k].(string); ok && s != "" {
				return truncate(oneLine(s), 200)
			}
		}
		return ""
	}
	return truncate(oneLine(string(body)), 200)
}

func checkOK(status int, body []byte) error {
	if status < 200 || status > 299 {
		return &Error{Code: "status", Status: status, Detail: serviceMessage(body)}
	}
	return nil
}

func checkTelegram(status int, body []byte) error {
	var v struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(body, &v)
	if status == 200 && v.OK {
		return nil
	}
	if status != 200 {
		return &Error{Code: "status", Status: status, Detail: truncate(oneLine(v.Description), 200)}
	}
	return &Error{Code: "rejected", Detail: truncate(oneLine(v.Description), 200)}
}

// checkCode checks a service that says how it went in a field of its JSON.
func checkCode(field string, want int) func(int, []byte) error {
	return func(status int, body []byte) error {
		if status < 200 || status > 299 {
			return &Error{Code: "status", Status: status, Detail: serviceMessage(body)}
		}
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			return &Error{Code: "rejected", Detail: serviceMessage(body)}
		}
		if n, ok := v[field].(float64); ok && int(n) == want {
			return nil
		}
		return &Error{Code: "rejected", Detail: serviceMessage(body)}
	}
}

// Timeout is how long a request may take.
const Timeout = 15 * time.Second

// DefaultClient is the client requests go through: no redirects, which
// would take the body elsewhere.
var DefaultClient = &http.Client{
	Timeout: Timeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Do makes r.
func Do(ctx context.Context, client *http.Client, r *Request) error {
	if client == nil {
		client = DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, strings.NewReader(r.Body))
	if err != nil {
		return &Error{Code: "bad_url"}
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		// A *url.Error says the address, which has secrets in it: keep
		// only what went wrong.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if ue != nil && ue.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			return &Error{Code: "timeout"}
		}
		return &Error{Code: "network", Detail: truncate(oneLine(err.Error()), 200)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if r.check == nil {
		return checkOK(resp.StatusCode, body)
	}
	return r.check(resp.StatusCode, body)
}

// Notifier sends notices somewhere.
type Notifier interface {
	Send(ctx context.Context, m Message) error
}

// channelNotifier sends through a channel, trying once more when it fails.
type channelNotifier struct {
	ch     Channel
	client *http.Client
	retry  time.Duration
}

// New returns the notifier of a channel, or why its values won't do.
func New(ch Channel, client *http.Client) (Notifier, error) {
	if _, err := Render(ch, Message{Title: "t", Body: "b"}); err != nil {
		return nil, err
	}
	return &channelNotifier{ch: ch, client: client, retry: time.Second}, nil
}

func (n *channelNotifier) Send(ctx context.Context, m Message) error {
	r, err := Render(n.ch, m)
	if err != nil {
		return err
	}
	err = Do(ctx, n.client, r)
	if err == nil || ctx.Err() != nil {
		return scrub(err, n.ch)
	}
	select {
	case <-ctx.Done():
		return scrub(err, n.ch)
	case <-time.After(n.retry):
	}
	return scrub(Do(ctx, n.client, r), n.ch)
}

// scrub takes the channel's secrets out of what an error says, should a
// service or the network repeat them.
func scrub(err error, ch Channel) error {
	var e *Error
	if !errors.As(err, &e) || e.Detail == "" {
		return err
	}
	for _, v := range ch.Secret {
		if len(v) >= 4 {
			e.Detail = strings.ReplaceAll(e.Detail, v, "***")
		}
	}
	return e
}

// ErrRepeat is a notice held back: the channel had one of the same event
// less than Window ago.
var ErrRepeat = errors.New("notify: the same event was sent less than 30 seconds ago")

// Window is how long a channel hears an event only once.
const Window = 30 * time.Second

// Dispatcher sends notices through channels, each event once in Window
// per channel. It's safe for any goroutine.
type Dispatcher struct {
	Client *http.Client
	// Retry is the wait before trying again.
	Retry time.Duration
	Now   func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewDispatcher returns a dispatcher with the default client.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{Client: DefaultClient, Retry: time.Second, Now: time.Now}
}

// Send sends m through ch, unless ch had the same event less than Window
// ago, which ErrRepeat says. Test notices always go.
func (d *Dispatcher) Send(ctx context.Context, ch Channel, m Message) error {
	if _, err := Render(ch, m); err != nil {
		return err
	}
	if m.Event != EventTest {
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		key := ch.ID + "\x00" + m.Event
		d.mu.Lock()
		if d.last == nil {
			d.last = map[string]time.Time{}
		}
		if t, ok := d.last[key]; ok && now.Sub(t) < Window {
			d.mu.Unlock()
			return ErrRepeat
		}
		d.last[key] = now
		for k, t := range d.last {
			if now.Sub(t) >= Window {
				delete(d.last, k)
			}
		}
		d.mu.Unlock()
	}
	n := &channelNotifier{ch: ch, client: d.Client, retry: d.Retry}
	return n.Send(ctx, m)
}
