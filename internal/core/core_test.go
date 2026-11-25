package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"colaunch/internal/colab"
)

// The watchdog's budget deadline is the earlier of the spending cap and
// the balance floor, at the account's rate, so it holds with the app
// closed.
func TestWatchdogBudget(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s := &sessionRecord{
		Created:      now.Add(-30 * time.Minute),
		StartBalance: 100,
		Guards:       Guards{StopAfter: 120, IdleStop: 20, SpendCap: 10, BalanceFloor: 50},
	}
	// 4 units spent so far, at 4 an hour: the cap leaves 6, 1.5 hours;
	// the floor leaves 46, 11.5 hours.
	w := watchdogFor(s, &colab.Quota{Balance: 96, RateHourly: 4}, now)
	if got, want := time.Unix(w.BudgetAt, 0).UTC(), now.Add(90*time.Minute); !got.Equal(want) {
		t.Errorf("budget at %v, want %v", got, want)
	}
	if got, want := time.Unix(w.StopAt, 0).UTC(), now.Add(90*time.Minute); !got.Equal(want) {
		t.Errorf("stop at %v, want %v", got, want)
	}
	if w.IdleMinutes != 20 {
		t.Errorf("idle %v", w.IdleMinutes)
	}
	// Past the floor already: stop now.
	w = watchdogFor(s, &colab.Quota{Balance: 40, RateHourly: 4}, now)
	if w.BudgetAt != now.Unix() {
		t.Errorf("budget at %v past the floor, want now", time.Unix(w.BudgetAt, 0))
	}
	// Nothing spent, as on the free tier: no budget deadline.
	w = watchdogFor(s, &colab.Quota{Balance: 0, RateHourly: 0}, now)
	if w.BudgetAt != 0 {
		t.Errorf("budget at %v with no rate", time.Unix(w.BudgetAt, 0))
	}
}

// Passing the spending cap stops the session even with the app open
// before the watchdog would.
func TestCheckBalanceStops(t *testing.T) {
	m := New(t.TempDir(), mapSecrets{})
	a := &Account{accountRecord: accountRecord{Email: "a@example.com"}, Quota: &colab.Quota{Balance: 88, RateHourly: 4}}
	s := &Session{Phase: PhaseRunning, sessionRecord: sessionRecord{Account: a.Email, StartBalance: 100, Guards: Guards{SpendCap: 10}}}
	m.Accounts = []*Account{a}
	m.Sessions = []*Session{s}
	m.checkBalance(a)
	if s.Phase != PhaseStopping || s.StopReason != "budget" {
		t.Errorf("phase %s, reason %q after spending 12 of 10", s.Phase, s.StopReason)
	}
}

// Older settings default to background operation, and an explicit opt-out
// survives restarting the app.
func TestBackgroundSettingPersistsOptOut(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, mapSecrets{})
	defer m.Close()
	if !m.Settings.BackgroundEnabled() {
		t.Fatal("background operation is disabled before a choice is saved")
	}
	m.Settings.Background = new(false)
	m.SaveSettings()
	restarted := New(dir, mapSecrets{})
	defer restarted.Close()
	if restarted.Settings.BackgroundEnabled() {
		t.Fatal("background operation was reenabled after restarting")
	}
}

func TestMachineSpec(t *testing.T) {
	got := Machine{Variant: "gpu", Accelerator: "A100", HighMem: true}.Spec()
	want := colab.Spec{Variant: colab.GPU, Accelerator: "A100", Shape: colab.HighMem}
	if got != want {
		t.Errorf("spec %+v", got)
	}
	if s := (Machine{Variant: "cpu", Accelerator: "T4"}).Spec(); s.Accelerator != "NONE" {
		t.Errorf("a CPU asked for %s", s.Accelerator)
	}
}

type mapSecrets map[string][]byte

func (m mapSecrets) Get(k string) ([]byte, error) { return m[k], nil }
func (m mapSecrets) Set(k string, v []byte) error { m[k] = v; return nil }
func (m mapSecrets) Delete(k string) error        { delete(m, k); return nil }

type call struct {
	email, acc string
}

// grabWith runs grab with try's answers, counting the rounds it waits.
func grabWith(t *testing.T, accounts []*Account, machine Machine, try func(call, int) error) (grabbed, []call, int, error) {
	m := New(t.TempDir(), mapSecrets{})
	waits := 0
	old := grabSleep
	grabSleep = func(context.Context, time.Duration) error { waits++; return nil }
	t.Cleanup(func() { grabSleep = old })
	var calls []call
	got, err := m.grab(context.Background(), &Session{}, accounts, machine, 3, func(a *Account, spec colab.Spec) error {
		c := call{a.Email, spec.Accelerator}
		calls = append(calls, c)
		return try(c, len(calls))
	})
	return got, calls, waits, err
}

func acct(email string) *Account {
	a := &Account{}
	a.Email = email
	return a
}

// Grabbing asks for each accelerator on each account, round after round,
// skipping an account out of quota, until one is assigned.
func TestGrabRotates(t *testing.T) {
	a1, a2 := acct("a1"), acct("a2")
	machine := Machine{Variant: "gpu", Accelerator: "A100", Fallbacks: []string{"L4", "A100"}, Grab: true}
	got, calls, waits, err := grabWith(t, []*Account{a1, a2}, machine, func(c call, n int) error {
		if c.email == "a1" {
			return colab.ErrQuota
		}
		if n < 5 {
			return fmt.Errorf("%w: %s", colab.ErrUnavailable, c.acc)
		}
		return nil
	})
	if err != nil || got.account != a2 || got.spec.Accelerator != "A100" || got.tries != 5 {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := []call{{"a1", "A100"}, {"a2", "A100"}, {"a2", "L4"}, {"a1", "A100"}, {"a2", "A100"}}
	if fmt.Sprint(calls) != fmt.Sprint(want) || waits != 1 {
		t.Errorf("calls %v, %d waits", calls, waits)
	}
}

// Without grabbing, one round of no accelerators fails at once, and an
// error that waiting won't fix fails even while grabbing.
func TestGrabGivesUp(t *testing.T) {
	_, calls, waits, err := grabWith(t, []*Account{acct("a")}, Machine{Variant: "gpu", Accelerator: "T4", Fallbacks: []string{"L4"}},
		func(call, int) error { return colab.ErrUnavailable })
	if !errors.Is(err, colab.ErrUnavailable) || len(calls) != 2 || waits != 0 {
		t.Errorf("no grab: %v, %d calls, %d waits", err, len(calls), waits)
	}
	_, calls, _, err = grabWith(t, []*Account{acct("a")}, Machine{Variant: "gpu", Accelerator: "T4", Grab: true},
		func(call, int) error { return colab.ErrDenylisted })
	if !errors.Is(err, colab.ErrDenylisted) || len(calls) != 1 {
		t.Errorf("denylisted: %v, %d calls", err, len(calls))
	}
	// Too little GPU memory is tried again, as many times as the settings say.
	_, calls, _, err = grabWith(t, []*Account{acct("a")}, Machine{Variant: "gpu", Accelerator: "A100", MinVRAM: 80},
		func(call, int) error { return errTooLittleVRAM })
	if !errors.Is(err, errTooLittleVRAM) || len(calls) != 3 {
		t.Errorf("vram: %v, %d calls", err, len(calls))
	}
}
