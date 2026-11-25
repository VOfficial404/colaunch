// Package google signs in to Google accounts with OAuth 2.0 for installed
// apps: the browser opens Google's consent page, which sends the code back
// to a loopback server on 127.0.0.1, or to the user, who pastes the address
// it lands on when the app could not keep its server running (iOS suspends
// an app in the background).
package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scopes are what Colab needs: the account's identity, and its runtimes.
var Scopes = []string{
	"profile",
	"email",
	"https://www.googleapis.com/auth/colaboratory",
}

var (
	authURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL    = "https://oauth2.googleapis.com/token"
	userInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	revokeURL   = "https://oauth2.googleapis.com/revoke"
)

// Client is an OAuth client of the "Desktop app" kind. Google's installed
// apps keep their secret in the open: it identifies the app, and guards
// nothing.
type Client struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// Valid reports whether the client has an ID.
func (c Client) Valid() bool { return strings.HasSuffix(c.ID, ".apps.googleusercontent.com") }

// Token is what Google hands back for an account.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

// Fresh reports whether the access token has more than a minute left.
func (t *Token) Fresh() bool {
	return t != nil && t.AccessToken != "" && time.Until(t.Expiry) > time.Minute
}

// UserInfo is the account a token belongs to.
type UserInfo struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// HTTP is the client every request goes through; tests replace it.
var HTTP = &http.Client{Timeout: 30 * time.Second}

// Login is one sign-in in progress: open URL in a browser, then Wait.
type Login struct {
	URL string

	client   Client
	verifier string
	state    string
	redirect string
	ln       net.Listener
	srv      *http.Server
	code     chan result
	once     sync.Once
}

type result struct {
	code string
	err  error
}

// StartLogin starts a loopback server and makes the consent page's address.
func StartLogin(c Client) (*Login, error) {
	if !c.Valid() {
		return nil, ErrNoClient
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &Login{
		client:   c,
		verifier: randomString(48),
		state:    randomString(16),
		redirect: fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port),
		ln:       ln,
		code:     make(chan result, 1),
	}
	sum := sha256.Sum256([]byte(l.verifier))
	q := url.Values{
		"client_id":             {c.ID},
		"redirect_uri":          {l.redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(Scopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent select_account"},
		"state":                 {l.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	l.URL = authURL + "?" + q.Encode()
	l.srv = &http.Server{Handler: http.HandlerFunc(l.serve), ReadHeaderTimeout: 10 * time.Second}
	go l.srv.Serve(ln)
	return l, nil
}

// ErrState is returned for an answer to another sign-in than this one's.
var ErrState = errors.New("google: the sign-in answered another request")

// serve answers the redirect: a GET of /, Google's answer. One with
// another state, which anything on this machine could send, is refused
// and leaves the sign-in waiting for Google's.
func (l *Login) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code, err := l.parse(r.URL.Query())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, page, "✗", htmlEscape(err.Error()))
		if errors.Is(err, ErrState) {
			return
		}
	} else {
		fmt.Fprintf(w, page, "✓", "Colaunch is signed in. You can close this tab. / 已登录，可以关闭此页面。")
	}
	l.finish(code, err)
}

// parse reads Google's answer. The state comes first: an error or a code
// in an answer to another request isn't this sign-in's.
func (l *Login) parse(q url.Values) (string, error) {
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(l.state)) != 1 {
		return "", ErrState
	}
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("google: %s", e)
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("google: no code in the answer")
	}
	return code, nil
}

func (l *Login) finish(code string, err error) {
	l.once.Do(func() { l.code <- result{code, err} })
}

// ErrPaste is returned for a pasted text that isn't the address the
// browser landed on, with its state: a bare code isn't taken, as nothing
// would tell that it answers this sign-in.
var ErrPaste = errors.New("google: paste the whole address the browser shows")

// Paste finishes the sign-in with the address the browser landed on, for
// when the loopback server could not answer it. The address must be the
// redirect's, with the sign-in's state.
func (l *Login) Paste(address string) error {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.RawQuery == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrPaste
	}
	want, _ := url.Parse(l.redirect)
	if u.Host != want.Host || (u.Path != "" && u.Path != "/") {
		return ErrPaste
	}
	q := u.Query()
	if q.Get("state") == "" {
		return ErrPaste
	}
	code, err := l.parse(q)
	if err != nil {
		if !errors.Is(err, ErrState) {
			l.finish("", err)
		}
		return err
	}
	l.finish(code, nil)
	return nil
}

// Cancel ends the sign-in.
func (l *Login) Cancel() { l.finish("", context.Canceled) }

// Wait waits for the code and trades it for a token and the account's info.
func (l *Login) Wait(ctx context.Context) (*Token, *UserInfo, error) {
	defer l.srv.Close()
	var r result
	select {
	case r = <-l.code:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if r.err != nil {
		return nil, nil, r.err
	}
	tok, err := l.client.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.code},
		"redirect_uri":  {l.redirect},
		"code_verifier": {l.verifier},
	})
	if err != nil {
		return nil, nil, err
	}
	if tok.RefreshToken == "" {
		return nil, nil, errors.New("google: no refresh token came back; remove Colaunch's access at myaccount.google.com/permissions and sign in again")
	}
	info, err := FetchUserInfo(ctx, tok.AccessToken)
	if err != nil {
		return nil, nil, err
	}
	return tok, info, nil
}

// Refresh trades a refresh token for a new access token.
func (c Client) Refresh(ctx context.Context, refresh string) (*Token, error) {
	tok, err := c.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
	})
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refresh
	}
	return tok, nil
}

// Revoke ends Colaunch's access to an account. Google answering anything
// but 200 is an error: the token may still work.
func Revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", revokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var r struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &r) == nil && r.Error != "" {
			return fmt.Errorf("google: revoking answered %s: %s %s", resp.Status, r.Error, r.Description)
		}
		return fmt.Errorf("google: revoking answered %s", resp.Status)
	}
	return nil
}

// ErrRevoked is returned when Google no longer accepts a refresh token.
var ErrRevoked = errors.New("google: the account's sign-in expired or was revoked; sign in again")

// ErrNoClient is returned when no OAuth client is configured.
var ErrNoClient = errors.New("google: no OAuth client is configured (Settings → OAuth client)")

func (c Client) exchange(ctx context.Context, form url.Values) (*Token, error) {
	form.Set("client_id", c.ID)
	if c.Secret != "" {
		form.Set("client_secret", c.Secret)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("google: token endpoint answered %s", resp.Status)
	}
	if r.Error == "invalid_grant" {
		return nil, ErrRevoked
	}
	if r.Error != "" || r.AccessToken == "" {
		return nil, fmt.Errorf("google: %s %s", r.Error, r.Description)
	}
	return &Token{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
	}, nil
}

// FetchUserInfo returns the account an access token belongs to.
func FetchUserInfo(ctx context.Context, access string) (*UserInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", userInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("google: userinfo answered %s", resp.Status)
	}
	var u UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	if u.Email == "" {
		return nil, errors.New("google: the account has no email")
	}
	return &u, nil
}

// TokenSource keeps an account's access token fresh.
type TokenSource struct {
	mu      sync.Mutex
	client  func() Client
	tok     Token
	onSaved func(Token)
}

// NewTokenSource makes a source from a refresh token. onSaved hears new
// tokens, to keep them.
func NewTokenSource(client func() Client, tok Token, onSaved func(Token)) *TokenSource {
	return &TokenSource{client: client, tok: tok, onSaved: onSaved}
}

// Token returns a fresh access token, refreshing it when needed.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok.Fresh() {
		return s.tok.AccessToken, nil
	}
	t, err := s.client().Refresh(ctx, s.tok.RefreshToken)
	if err != nil {
		return "", err
	}
	s.tok = *t
	if s.onSaved != nil {
		s.onSaved(*t)
	}
	return t.AccessToken, nil
}

// Expire forgets the access token, after the server refused it.
func (s *TokenSource) Expire() {
	s.mu.Lock()
	s.tok.Expiry = time.Time{}
	s.mu.Unlock()
}

// Current returns the token as it is.
func (s *TokenSource) Current() Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

const page = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Colaunch</title><style>body{font:16px system-ui;display:grid;place-items:center;height:90vh;color:#222}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}}b{font-size:48px;display:block;text-align:center}</style>
<div><b>%s</b><p>%s</p></div>`
