package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/google"
)

var (
	// errTooLittleVRAM is a runtime given back for having less GPU memory
	// than asked.
	errTooLittleVRAM = errors.New("not enough GPU memory")
	// errConnect is a runtime given back because its terminal didn't
	// answer.
	errConnect = errors.New("couldn't connect to the runtime")
)

// grabSleep waits between rounds; tests replace it.
var grabSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// grabbed is what grab got.
type grabbed struct {
	account *Account
	spec    colab.Spec
	tries   int
	waited  time.Duration
}

// grab asks for a runtime as the machine says: each of its accelerators,
// in order, on each account it may use, round after round while grabbing,
// until try takes one. try assigns a runtime and keeps it, or gives it back
// and says why. vramTries is how many runtimes with too little GPU memory
// to give back before giving up, when not grabbing.
func (m *Manager) grab(ctx context.Context, s *Session, accounts []*Account, machine Machine, vramTries int,
	try func(a *Account, spec colab.Spec) error) (grabbed, error) {
	start := time.Now()
	interval := time.Duration(max(machine.RetrySeconds, 10) * float64(time.Second))
	attempt, vramMisses := 0, 0
	for round := 1; ; round++ {
		var last error
		for _, a := range accounts {
		candidates:
			for _, acc := range machine.Candidates() {
				spec := machine.Spec()
				spec.Accelerator = acc
				attempt++
				n, r, email := attempt, round, a.Email
				m.Post(func() {
					s.Phase, s.Attempt, s.Round, s.Spec, s.Account = PhaseAssigning, n, r, spec, email
				})
				m.logf(s, "Assigning %s on %s (try %d)…", spec, email, n)
				err := try(a, spec)
				if err == nil {
					return grabbed{account: a, spec: spec, tries: attempt, waited: time.Since(start)}, nil
				}
				if ctx.Err() != nil {
					return grabbed{}, ctx.Err()
				}
				m.logf(s, "✗ %s", errText(err))
				msg := errText(err)
				m.Post(func() { s.TryErr = msg })
				last = err
				switch {
				case errors.Is(err, errTooLittleVRAM):
					vramMisses++
				case errors.Is(err, colab.ErrUnavailable), errors.Is(err, errConnect), transient(err):
				case errors.Is(err, colab.ErrTooManyRuntimes), errors.Is(err, colab.ErrQuota):
					// The account's other accelerators fare no better.
					break candidates
				case len(accounts) > 1 && accountErr(err):
					break candidates
				default:
					return grabbed{}, err
				}
			}
		}
		if !machine.Grab {
			if errors.Is(last, errTooLittleVRAM) && vramMisses < vramTries {
				if err := grabSleep(ctx, 5*time.Second); err != nil {
					return grabbed{}, err
				}
				continue
			}
			if errors.Is(last, colab.ErrTooManyRuntimes) {
				last = fmt.Errorf("%w: stop one of its runtimes first", last)
			}
			return grabbed{}, last
		}
		if machine.MaxWait > 0 && time.Since(start) >= time.Duration(machine.MaxWait*float64(time.Minute)) {
			return grabbed{}, fmt.Errorf("gave up after %d tries in %s: %w", attempt, time.Since(start).Round(time.Second), last)
		}
		next := time.Now().Add(interval)
		m.Post(func() { s.Phase, s.NextTry = PhaseWaiting, next })
		if err := grabSleep(ctx, interval); err != nil {
			return grabbed{}, err
		}
	}
}

// transient reports whether an error may go away by itself: the network's,
// or Colab being busy.
func transient(err error) bool {
	var api *colab.APIError
	if errors.As(err, &api) {
		// RESOURCE_EXHAUSTED, UNAVAILABLE, DEADLINE_EXCEEDED.
		return api.Status == 429 || api.Status >= 500 || api.Code == 8 || api.Code == 14 || api.Code == 4
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// accountErr reports whether an error is the account's, which another
// account may not have.
func accountErr(err error) bool {
	return errors.Is(err, colab.ErrDenylisted) || errors.Is(err, colab.ErrUnauthorized) || errors.Is(err, google.ErrRevoked)
}

// grabAccounts are the accounts to try: the active one, then, when the
// machine may use any, the others that are signed in.
func (m *Manager) grabAccounts(active *Account, any bool) []*Account {
	out := []*Account{active}
	if !any {
		return out
	}
	for _, a := range m.Accounts {
		if a != active && !a.SignedOut {
			out = append(out, a)
		}
	}
	return out
}
