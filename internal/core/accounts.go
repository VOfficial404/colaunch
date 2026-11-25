package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/google"
)

// Account is a Google account and what Colab says of it.
type Account struct {
	accountRecord
	Client *colab.Client
	ts     *google.TokenSource

	Quota    *colab.Quota
	QuotaErr string
	Specs    []colab.RuntimeSpec
	Runtimes []colab.Runtime
	// SignedOut is true once Google no longer takes its sign-in.
	SignedOut  bool
	Refreshing bool
}

func (m *Manager) newAccount(r accountRecord, tok *google.Token) *Account {
	a := &Account{accountRecord: r}
	if tok == nil {
		tok = &google.Token{}
		if b, err := m.Secrets.Get("account/" + r.Email); err == nil {
			json.Unmarshal(b, tok)
		}
	}
	if tok.RefreshToken == "" {
		a.SignedOut = true
	}
	email := r.Email
	a.ts = google.NewTokenSource(m.Client, *tok, func(t google.Token) {
		b, _ := json.Marshal(google.Token{RefreshToken: t.RefreshToken})
		m.Secrets.Set("account/"+email, b)
	})
	a.Client = &colab.Client{
		Token:   a.ts.Token,
		Expired: a.ts.Expire,
		HTTP:    m.http,
		AppName: "Colaunch",
	}
	return a
}

// Login is a sign-in the user is going through.
type Login struct {
	*google.Login
	cancel context.CancelFunc
}

// StartLogin starts adding an account: open the login's URL in a browser.
// done hears the account, or why there is none, on the main thread.
func (m *Manager) StartLogin(done func(*Account, error)) (*Login, error) {
	gl, err := google.StartLogin(m.Client())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	l := &Login{Login: gl, cancel: cancel}
	go func() {
		defer cancel()
		tok, info, err := gl.Wait(ctx)
		m.Post(func() {
			if err != nil {
				done(nil, err)
				return
			}
			done(m.addAccount(tok, info), nil)
		})
	}()
	return l, nil
}

// Cancel abandons the sign-in.
func (l *Login) Cancel() {
	l.Login.Cancel()
	l.cancel()
}

func (m *Manager) addAccount(tok *google.Token, info *google.UserInfo) *Account {
	b, _ := json.Marshal(google.Token{RefreshToken: tok.RefreshToken})
	m.Secrets.Set("account/"+info.Email, b)
	rec := accountRecord{Email: info.Email, Name: info.Name, Picture: info.Picture, Added: time.Now()}
	a := m.newAccount(rec, tok)
	for i, old := range m.Accounts {
		if old.Email == info.Email {
			a.Added = old.Added
			m.Accounts[i] = a
			m.saveState()
			go m.refreshAccount(a)
			return a
		}
	}
	m.Accounts = append(m.Accounts, a)
	if m.Settings.ActiveAccount == "" {
		m.SetActive(a.Email)
	}
	m.saveState()
	go m.refreshAccount(a)
	return a
}

// RemoveAccount signs an account out of Colaunch. Its runtimes keep
// running, with their watchdogs.
func (m *Manager) RemoveAccount(a *Account) {
	tok := a.ts.Current().RefreshToken
	go func() {
		if tok != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			google.Revoke(ctx, tok)
			cancel()
		}
	}()
	m.Secrets.Delete("account/" + a.Email)
	for i, x := range m.Accounts {
		if x == a {
			m.Accounts = append(m.Accounts[:i], m.Accounts[i+1:]...)
			break
		}
	}
	if m.Settings.ActiveAccount == a.Email {
		m.Settings.ActiveAccount = ""
		if len(m.Accounts) > 0 {
			m.Settings.ActiveAccount = m.Accounts[0].Email
		}
		m.SaveSettings()
	}
	m.saveState()
}

// Refresh asks Colab for an account's balance and runtimes again.
func (m *Manager) Refresh(a *Account) { go m.refreshAccount(a) }

func (m *Manager) refreshAccount(a *Account) {
	if a.Client == nil {
		return
	}
	m.Post(func() { a.Refreshing = true })
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	q, qerr := a.Client.Quota(ctx)
	var specs []colab.RuntimeSpec
	var rts []colab.Runtime
	var rerr error
	if qerr == nil {
		specs, _ = a.Client.RuntimeSpecs(ctx)
		rts, rerr = a.Client.Runtimes(ctx)
	}
	m.Post(func() {
		a.Refreshing = false
		if errors.Is(qerr, google.ErrRevoked) {
			a.SignedOut = true
		}
		if qerr != nil {
			a.QuotaErr = errText(qerr)
			return
		}
		a.SignedOut = false
		a.QuotaErr = ""
		a.Quota = q
		if len(specs) > 0 {
			a.Specs = specs
		}
		if rerr == nil {
			a.Runtimes = rts
		}
		m.checkBalance(a)
	})
}

// Accelerators returns the accelerators of a variant the account can
// assign, and those it can't.
func (a *Account) Accelerators(variant colab.Variant) (eligible, ineligible []string) {
	seen := map[string]bool{}
	for _, s := range a.Specs {
		if s.Key.Variant != variant || s.Key.Accelerator == "" || s.Key.Accelerator == "NONE" || seen[s.Key.Accelerator] {
			continue
		}
		seen[s.Key.Accelerator] = true
		if s.Eligible {
			eligible = append(eligible, s.Key.Accelerator)
		} else {
			ineligible = append(ineligible, s.Key.Accelerator)
		}
	}
	if len(seen) > 0 || a.Quota == nil {
		return
	}
	// Without specs, the quota lists them too.
	v := string(variant)
	for _, acc := range a.Quota.Eligible {
		if acc.Variant == v {
			eligible = append(eligible, acc.Models...)
		}
	}
	for _, acc := range a.Quota.Ineligible {
		if acc.Variant == v {
			ineligible = append(ineligible, acc.Models...)
		}
	}
	return
}

// Label is how the account shows.
func (a *Account) Label() string {
	if a.Name != "" {
		return a.Name + " · " + a.Email
	}
	return a.Email
}
