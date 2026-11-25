package core

import (
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
