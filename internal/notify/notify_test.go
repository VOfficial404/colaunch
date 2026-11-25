package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// seen is a request a fake service got.
type seen struct {
	Method string
	URL    string // as the client asked for it
	Header http.Header
	Body   map[string]any
}

// fakeService answers like a service, and keeps what it got. Requests
// for any address go to it, so that the services' own addresses, which
// are fixed, are seen as the client asked for them.
type fakeService struct {
	mu     sync.Mutex
	got    []seen
	status int
	answer string
}

func (f *fakeService) client(t *testing.T) *http.Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("body isn't JSON: %s", b)
		}
		f.mu.Lock()
		f.got = append(f.got, seen{r.Method, r.Header.Get("X-Original-URL"), r.Header.Clone(), body})
		status, answer := f.status, f.answer
		f.mu.Unlock()
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(ts.Close)
	target, _ := url.Parse(ts.URL)
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Original-URL", r.URL.String())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var msg = Message{Event: EventReady, Title: "Ready: Qwen3-8B", Body: "A100 · 80 GB", Time: time.Date(2026, 5, 1, 8, 30, 0, 0, time.UTC)}

// Each kind of channel sends the request its service documents.
func TestRequestFormats(t *testing.T) {
	cases := []struct {
		name    string
		ch      Channel
		answer  string
		status  int
		url     string
		body    map[string]any
		headers map[string]string
	}{
		{
			name:   "telegram",
			ch:     Channel{Kind: KindTelegram, Params: map[string]string{"chat_id": "-100123"}, Secret: map[string]string{"bot_token": "123456:ABC-def_g"}},
			answer: `{"ok":true,"result":{}}`,
			url:    "https://api.telegram.org/bot123456:ABC-def_g/sendMessage",
			body:   map[string]any{"chat_id": "-100123", "text": "Ready: Qwen3-8B\nA100 · 80 GB"},
		},
		{
			name:   "telegram through a proxy",
			ch:     Channel{Kind: KindTelegram, Params: map[string]string{"chat_id": "42", "api_base": "https://tg.example.com/api/"}, Secret: map[string]string{"bot_token": "1:x"}},
			answer: `{"ok":true}`,
			url:    "https://tg.example.com/api/bot1:x/sendMessage",
			body:   map[string]any{"chat_id": "42", "text": "Ready: Qwen3-8B\nA100 · 80 GB"},
		},
		{
			name:   "bark",
			ch:     Channel{Kind: KindBark, Params: map[string]string{"sound": "minuet", "level": "timeSensitive"}, Secret: map[string]string{"device_key": "ynJ5Ft4atkMkWeo2PAvFhF"}},
			answer: `{"code":200,"message":"success","timestamp":1}`,
			url:    "https://api.day.app/push",
			body:   map[string]any{"device_key": "ynJ5Ft4atkMkWeo2PAvFhF", "title": "Ready: Qwen3-8B", "body": "A100 · 80 GB", "group": "Colaunch", "sound": "minuet", "level": "timeSensitive"},
		},
		{
			name:   "bark of one's own, over http on the LAN",
			ch:     Channel{Kind: KindBark, Params: map[string]string{"server": "http://192.168.1.5:8080", "group": "gpu"}, Secret: map[string]string{"device_key": "k1"}},
			answer: `{"code":200}`,
			url:    "http://192.168.1.5:8080/push",
			body:   map[string]any{"device_key": "k1", "title": "Ready: Qwen3-8B", "body": "A100 · 80 GB", "group": "gpu"},
		},
		{
			name:   "serverchan",
			ch:     Channel{Kind: KindServerChan, Secret: map[string]string{"sendkey": "SCT123abc"}},
			answer: `{"code":0,"message":"","data":{}}`,
			url:    "https://sctapi.ftqq.com/SCT123abc.send",
			body:   map[string]any{"title": "Ready: Qwen3-8B", "desp": "A100 · 80 GB"},
		},
		{
			name:   "serverchan³",
			ch:     Channel{Kind: KindServerChan, Secret: map[string]string{"sendkey": "sctp4321tABCdef"}},
			answer: `{"code":0}`,
			url:    "https://4321.push.ft07.com/send/sctp4321tABCdef.send",
			body:   map[string]any{"title": "Ready: Qwen3-8B", "desp": "A100 · 80 GB"},
		},
		{
			name:   "pushplus",
			ch:     Channel{Kind: KindPushPlus, Secret: map[string]string{"token": "abc123"}},
			answer: `{"code":200,"msg":"请求成功","data":"x"}`,
			url:    "https://www.pushplus.plus/send",
			body:   map[string]any{"token": "abc123", "title": "Ready: Qwen3-8B", "content": "A100 · 80 GB", "template": "txt"},
		},
		{
			name:    "ntfy",
			ch:      Channel{Kind: KindNtfy, Secret: map[string]string{"topic": "colaunch_x1", "token": "tk_abc"}},
			url:     "https://ntfy.sh/",
			body:    map[string]any{"topic": "colaunch_x1", "title": "Ready: Qwen3-8B", "message": "A100 · 80 GB", "tags": []any{"colaunch"}},
			headers: map[string]string{"Authorization": "Bearer tk_abc"},
		},
		{
			name:   "discord",
			ch:     Channel{Kind: KindDiscord, Secret: map[string]string{"url": "https://discord.com/api/webhooks/1/abc"}},
			status: 204,
			url:    "https://discord.com/api/webhooks/1/abc",
			body:   map[string]any{"content": "**Ready: Qwen3-8B**\nA100 · 80 GB", "allowed_mentions": map[string]any{"parse": []any{}}},
		},
		{
			name:    "webhook",
			ch:      Channel{Kind: KindWebhook, Secret: map[string]string{"url": "https://hooks.example.com/x?k=1", "headers": "X-Token: s3cret\nX-Other:  two "}},
			url:     "https://hooks.example.com/x?k=1",
			body:    map[string]any{"event": "ready", "title": "Ready: Qwen3-8B", "body": "A100 · 80 GB", "time": "2026-05-01T08:30:00Z"},
			headers: map[string]string{"X-Token": "s3cret", "X-Other": "two"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeService{status: c.status, answer: c.answer}
			n, err := New(c.ch, f.client(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := n.Send(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if len(f.got) != 1 {
				t.Fatalf("%d requests", len(f.got))
			}
			g := f.got[0]
			if g.Method != "POST" || g.URL != c.url {
				t.Errorf("%s %s, want POST %s", g.Method, g.URL, c.url)
			}
			if ct := g.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type %q", ct)
			}
			want, _ := json.Marshal(c.body)
			got, _ := json.Marshal(g.Body)
			if string(want) != string(got) {
				t.Errorf("body\n got %s\nwant %s", got, want)
			}
			for k, v := range c.headers {
				if g.Header.Get(k) != v {
					t.Errorf("header %s is %q, want %q", k, g.Header.Get(k), v)
				}
			}
		})
	}
}

// A service that says no is an error, with what it said and without the
// channel's secrets; a failure is tried once more.
func TestFailures(t *testing.T) {
	f := &fakeService{status: 401, answer: `{"ok":false,"error_code":401,"description":"Unauthorized"}`}
	ch := Channel{Kind: KindTelegram, Params: map[string]string{"chat_id": "1"}, Secret: map[string]string{"bot_token": "99:SECRETTOKEN"}}
	d := &Dispatcher{Client: f.client(t), Retry: time.Millisecond}
	err := d.Send(context.Background(), ch, msg)
	var e *Error
	if !errors.As(err, &e) || e.Code != "status" || e.Status != 401 || e.Detail != "Unauthorized" {
		t.Fatalf("got %v", err)
	}
	if len(f.got) != 2 {
		t.Errorf("tried %d times, want 2", len(f.got))
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("the error says the token: %v", err)
	}

	// 200 with ok false.
	f = &fakeService{answer: `{"ok":false,"description":"Bad Request: chat not found"}`}
	err = (&Dispatcher{Client: f.client(t), Retry: time.Millisecond}).Send(context.Background(), ch, msg)
	if !errors.As(err, &e) || e.Code != "rejected" || !strings.Contains(e.Detail, "chat not found") {
		t.Errorf("got %v", err)
	}
	// Bark's code, PushPlus's code.
	for _, c := range []struct {
		ch     Channel
		answer string
	}{
		{Channel{Kind: KindBark, Secret: map[string]string{"device_key": "k"}}, `{"code":400,"message":"failed to get device token"}`},
		{Channel{Kind: KindPushPlus, Secret: map[string]string{"token": "t"}}, `{"code":903,"msg":"无效的用户token"}`},
		{Channel{Kind: KindServerChan, Secret: map[string]string{"sendkey": "SCTx"}}, `{"code":40001,"message":"bad pushkey"}`},
	} {
		f := &fakeService{answer: c.answer}
		err := (&Dispatcher{Client: f.client(t), Retry: time.Millisecond}).Send(context.Background(), c.ch, msg)
		if !errors.As(err, &e) || e.Code != "rejected" || e.Detail == "" {
			t.Errorf("%s: got %v", c.ch.Kind, err)
		}
	}

	// Once more after a failure, and then it goes.
	var n int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()
	wh := Channel{Kind: KindWebhook, Secret: map[string]string{"url": ts.URL + "/hook"}}
	if err := (&Dispatcher{Client: ts.Client(), Retry: time.Millisecond}).Send(context.Background(), wh, msg); err != nil || n != 2 {
		t.Errorf("after a 502: %v, %d requests", err, n)
	}

	// A network error doesn't say the address, which has the token.
	dead := Channel{Kind: KindTelegram, Params: map[string]string{"chat_id": "1", "api_base": "http://127.0.0.1:1"}, Secret: map[string]string{"bot_token": "99:SECRETTOKEN"}}
	err = (&Dispatcher{Retry: time.Millisecond}).Send(context.Background(), dead, msg)
	if !errors.As(err, &e) || e.Code != "network" || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("got %v", err)
	}
}

// The same event goes through a channel once in 30 seconds; other events,
// other channels and tests aren't held back.
func TestRepeatsHeldBack(t *testing.T) {
	f := &fakeService{}
	now := time.Unix(1000, 0)
	d := &Dispatcher{Client: f.client(t), Retry: time.Millisecond, Now: func() time.Time { return now }}
	a := Channel{ID: "a", Kind: KindWebhook, Secret: map[string]string{"url": "https://h.example.com/a"}}
	b := a
	b.ID = "b"
	ctx := context.Background()
	stopped := Message{Event: EventStopped, Title: "x"}
	if err := d.Send(ctx, a, stopped); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * time.Second)
	if err := d.Send(ctx, a, stopped); !errors.Is(err, ErrRepeat) {
		t.Errorf("a repeat within 30 s: %v", err)
	}
	if err := d.Send(ctx, b, stopped); err != nil {
		t.Errorf("another channel: %v", err)
	}
	if err := d.Send(ctx, a, Message{Event: EventReady}); err != nil {
		t.Errorf("another event: %v", err)
	}
	d.Send(ctx, a, Message{Event: EventTest})
	if err := d.Send(ctx, a, Message{Event: EventTest}); err != nil {
		t.Errorf("a second test: %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := d.Send(ctx, a, stopped); err != nil {
		t.Errorf("after 31 s: %v", err)
	}
	if len(f.got) != 6 {
		t.Errorf("%d requests, want 6", len(f.got))
	}
}

// Addresses are https, or http to this machine or a private network.
func TestCheckURL(t *testing.T) {
	for _, ok := range []string{
		"https://api.day.app", "https://tg.example.com/proxy", "http://127.0.0.1:8080",
		"http://localhost:2", "http://10.0.0.2", "http://172.20.1.1", "http://192.168.1.5:80", "http://[::1]:8080", "http://[fd00::1]",
	} {
		if _, err := CheckURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://api.day.app", "http://8.8.8.8", "http://172.32.0.1", "http://nas.local", "ftp://x", "file:///etc/passwd",
		"api.day.app", "", "https://user:pw@example.com", "javascript:alert(1)",
	} {
		if _, err := CheckURL(bad); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	// A channel with an http address on the Internet doesn't send.
	ch := Channel{Kind: KindNtfy, Params: map[string]string{"server": "http://ntfy.example.com"}, Secret: map[string]string{"topic": "t"}}
	var e *Error
	if _, err := New(ch, nil); !errors.As(err, &e) || e.Code != "insecure_url" || e.Field != "server" {
		t.Errorf("got %v", err)
	}
}

// What each kind requires, and values that would change the address.
func TestValues(t *testing.T) {
	var e *Error
	if _, err := New(Channel{Kind: KindTelegram, Secret: map[string]string{"bot_token": "1:a"}}, nil); !errors.As(err, &e) || e.Code != "missing" || e.Field != "chat_id" {
		t.Errorf("no chat id: %v", err)
	}
	for _, bad := range []Channel{
		{Kind: KindTelegram, Params: map[string]string{"chat_id": "1"}, Secret: map[string]string{"bot_token": "1:a/../x"}},
		{Kind: KindBark, Secret: map[string]string{"device_key": "a/b"}},
		{Kind: KindServerChan, Secret: map[string]string{"sendkey": "a?b"}},
		{Kind: KindNtfy, Secret: map[string]string{"topic": "a/b"}},
		{Kind: KindWebhook, Secret: map[string]string{"url": "https://x.example.com", "headers": "no colon"}},
		{Kind: KindWebhook, Secret: map[string]string{"url": "https://x.example.com", "headers": "Host: evil"}},
		{Kind: "pager"},
	} {
		if _, err := New(bad, nil); err == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
	// Server酱's title is a line of 32 characters at most.
	r, err := Render(Channel{Kind: KindServerChan, Secret: map[string]string{"sendkey": "SCT1"}}, Message{Title: "a\nb" + strings.Repeat("x", 40)})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.Unmarshal([]byte(r.Body), &body)
	if strings.Contains(body["title"], "\n") || len([]rune(body["title"])) > 32 {
		t.Errorf("title %q", body["title"])
	}
}

// Requests for the runtime keep the placeholders for it to fill in.
func TestRenderForRuntime(t *testing.T) {
	ch := Channel{Kind: KindWebhook, Secret: map[string]string{"url": "https://h.example.com"}}
	r, err := Render(ch, Message{Event: EventStopped, Title: "Stopped: <m>", Body: "Reason: " + ReasonPlaceholder})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Body, `"body":"Reason: {reason}"`) || !strings.Contains(r.Body, `"time":"{time}"`) || !strings.Contains(r.Body, "<m>") {
		t.Errorf("body %s", r.Body)
	}
}

// Secret values leave the channel, wherever they were.
func TestSplit(t *testing.T) {
	ch := Channel{ID: "x", Kind: KindTelegram, Params: map[string]string{"chat_id": "1", "bot_token": "1:a"}}
	pub, sec := Split(ch)
	if pub.Params["bot_token"] != "" || pub.Secret != nil || sec["bot_token"] != "1:a" || pub.Params["chat_id"] != "1" {
		t.Errorf("split into %+v and %v", pub, sec)
	}
}
