package google

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newLogin(t *testing.T) *Login {
	t.Helper()
	l, err := StartLogin(Client{ID: "x.apps.googleusercontent.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.srv.Close() })
	return l
}

// pending returns what the sign-in got, if anything yet.
func pending(l *Login) (result, bool) {
	select {
	case r := <-l.code:
		return r, true
	case <-time.After(20 * time.Millisecond):
		return result{}, false
	}
}

func get(l *Login, method, target string) int {
	w := httptest.NewRecorder()
	l.serve(w, httptest.NewRequest(method, target, nil))
	return w.Code
}

// The loopback server takes Google's answer, a GET of / with the state,
// and nothing else ends the sign-in.
func TestCallbackServer(t *testing.T) {
	l := newLogin(t)
	q := "/?" + url.Values{"state": {l.state}, "code": {"c0de"}}.Encode()
	if c := get(l, "POST", q); c != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", c)
	}
	if c := get(l, "GET", "/other"+q[1:]); c != http.StatusNotFound {
		t.Errorf("another path: %d", c)
	}
	if c := get(l, "GET", "/?state=wrong&code=evil"); c != http.StatusBadRequest {
		t.Errorf("another state: %d", c)
	}
	if c := get(l, "GET", "/?state=wrong&error=access_denied"); c != http.StatusBadRequest {
		t.Errorf("an error with another state: %d", c)
	}
	if c := get(l, "GET", "/?code=evil"); c != http.StatusBadRequest {
		t.Errorf("no state: %d", c)
	}
	if r, ok := pending(l); ok {
		t.Fatalf("the sign-in ended with %+v", r)
	}
	if c := get(l, "GET", q); c != http.StatusOK {
		t.Errorf("Google's answer: %d", c)
	}
	if r, ok := pending(l); !ok || r.code != "c0de" || r.err != nil {
		t.Errorf("got %+v %v", r, ok)
	}
}

// Google refusing, with the sign-in's state, ends it.
func TestCallbackError(t *testing.T) {
	l := newLogin(t)
	get(l, "GET", "/?"+url.Values{"state": {l.state}, "error": {"access_denied"}}.Encode())
	if r, ok := pending(l); !ok || r.err == nil {
		t.Errorf("got %+v %v", r, ok)
	}
}

// A pasted address must be the redirect with the sign-in's state; a bare
// code isn't taken.
func TestPaste(t *testing.T) {
	l := newLogin(t)
	for _, s := range []string{
		"c0de",
		"4/0AbCdEf",
		l.redirect + "/?code=c0de",
		l.redirect + "/?state=wrong&code=c0de",
		"https://evil.example/?" + url.Values{"state": {l.state}, "code": {"c0de"}}.Encode(),
		"",
	} {
		if err := l.Paste(s); err == nil {
			t.Errorf("%q was taken", s)
		}
	}
	if r, ok := pending(l); ok {
		t.Fatalf("the sign-in ended with %+v", r)
	}
	if err := l.Paste(" " + l.redirect + "/?" + url.Values{"state": {l.state}, "code": {"c0de"}, "scope": {"email"}}.Encode() + "\n"); err != nil {
		t.Fatal(err)
	}
	if r, ok := pending(l); !ok || r.code != "c0de" {
		t.Errorf("got %+v %v", r, ok)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRevokeChecksTheAnswer(t *testing.T) {
	old := HTTP
	t.Cleanup(func() { HTTP = old })
	status := 0
	HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"error": "invalid_token"}`))}, nil
	})}
	status = 400
	if err := Revoke(context.Background(), "t"); err == nil || !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("400: %v", err)
	}
	status = 503
	if err := Revoke(context.Background(), "t"); err == nil {
		t.Error("503 isn't an error")
	}
	status = 200
	if err := Revoke(context.Background(), "t"); err != nil {
		t.Errorf("200: %v", err)
	}
}
