// Package core is Colaunch without its interface: the accounts, the
// runtimes it assigned and what runs on them, and the limits on what they
// cost. Its state changes on the main thread only, through Manager.Post, so
// that the view reads it without locks.
package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"colaunch/internal/colab"
	"colaunch/internal/google"
	"colaunch/internal/plugins"
)

// Secrets keeps what shouldn't sit in a file: the system's keychain in the
// app, a map in tests.
type Secrets interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Settings are the app's settings.
type Settings struct {
	// Language is "", following the system, "en", "zh-Hans" or "zh-Hant".
	Language string `json:"language"`
	// Theme is "system", "light" or "dark".
	Theme string `json:"theme"`
	// Background is enabled by default, including for settings saved before it existed.
	Background *bool `json:"background,omitempty"`
	// OAuth is the client accounts sign in with.
	OAuth google.Client `json:"oauth"`
	// HFEndpoint is Hugging Face's address, for mirrors.
	HFEndpoint string `json:"hf_endpoint,omitempty"`
	// LlamaCUDAURL is where prebuilt llama.cpp for CUDA downloads from.
	LlamaCUDAURL string `json:"llama_cuda_url,omitempty"`
	// ActiveAccount is the email of the account new runtimes go to.
	ActiveAccount string `json:"active_account"`
	// MaxAttempts is how many times to assign a GPU before giving up
	// waiting for one with enough memory.
	MaxAttempts int `json:"max_attempts"`
	// KeepFailed keeps a runtime whose plugin failed, to look into it,
	// instead of giving it back.
	KeepFailed bool `json:"keep_failed"`
	// Deploy is the deploy form as it was last.
	Deploy DeployForm `json:"deploy"`
}

func (s Settings) BackgroundEnabled() bool {
	return s.Background == nil || *s.Background
}

// DeployForm is what the Deploy page asks.
type DeployForm struct {
	Plugin string                       `json:"plugin"`
	Values map[string]map[string]string `json:"values"`
	Machine
	Guards
}

// Machine is the hardware asked for.
type Machine struct {
	// Variant is "cpu", "gpu" or "tpu".
	Variant     string `json:"variant"`
	Accelerator string `json:"accelerator"`
	HighMem     bool   `json:"high_mem"`
	// MinVRAM is the least GPU memory to keep a runtime, in GB: a
	// runtime with less is given back and another assigned, as A100s come
	// with 40 or 80.
	MinVRAM float64 `json:"min_vram"`
	// Fallbacks are accelerators to ask for, in order, when Accelerator
	// isn't available.
	Fallbacks []string `json:"fallbacks,omitempty"`
	// Grab keeps asking, round after round, until a runtime is assigned.
	Grab bool `json:"grab"`
	// RetrySeconds is the wait between rounds while grabbing.
	RetrySeconds float64 `json:"retry_seconds"`
	// MaxWait gives up grabbing after so many minutes, 0 for never.
	MaxWait float64 `json:"max_wait"`
	// AnyAccount asks on the other accounts too.
	AnyAccount bool `json:"any_account"`
}

// Candidates are the accelerators to ask for, in order.
func (m Machine) Candidates() []string {
	first := m.Spec().Accelerator
	out := []string{first}
	if first == "NONE" {
		return out
	}
	for _, f := range m.Fallbacks {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// Spec is the Colab spec of the machine.
func (m Machine) Spec() colab.Spec {
	s := colab.Spec{Variant: colab.CPU, Accelerator: "NONE", Shape: colab.Standard}
	switch m.Variant {
	case "gpu":
		s.Variant = colab.GPU
		s.Accelerator = m.Accelerator
	case "tpu":
		s.Variant = colab.TPU
		s.Accelerator = m.Accelerator
	}
	if s.Accelerator == "" {
		s.Accelerator = "NONE"
	}
	if m.HighMem {
		s.Shape = colab.HighMem
	}
	return s
}

// Guards are the limits on what a runtime costs. Zero turns one off.
type Guards struct {
	// StopAfter stops the runtime this many minutes after it starts.
	StopAfter float64 `json:"stop_after"`
	// IdleStop stops it after this many minutes without use.
	IdleStop float64 `json:"idle_stop"`
	// SpendCap stops it once the account has spent this many compute
	// units since it started.
	SpendCap float64 `json:"spend_cap"`
	// BalanceFloor stops it once the account's balance is down to this.
	BalanceFloor float64 `json:"balance_floor"`
}

// Any reports whether any guard is on.
func (g Guards) Any() bool {
	return g.StopAfter > 0 || g.IdleStop > 0 || g.SpendCap > 0 || g.BalanceFloor > 0
}

func defaultSettings() Settings {
	return Settings{
		Theme:       "system",
		MaxAttempts: 6,
		Deploy: DeployForm{
			Plugin:  "llama",
			Values:  map[string]map[string]string{},
			Machine: Machine{Variant: "gpu", Accelerator: "T4", RetrySeconds: 30},
			Guards:  Guards{IdleStop: 30, StopAfter: 240},
		},
	}
}

// accountRecord is an account as it's kept; its refresh token is in
// Secrets.
type accountRecord struct {
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	Picture string    `json:"picture"`
	Added   time.Time `json:"added"`
}

// sessionRecord is a session as it's kept, to find its runtime again
// after the app restarts.
type sessionRecord struct {
	ID           string            `json:"id"`
	RuntimeID    string            `json:"runtime_id"`
	Account      string            `json:"account"`
	Spec         colab.Spec        `json:"spec"`
	Plugin       string            `json:"plugin"`
	Values       map[string]string `json:"values"`
	Guards       Guards            `json:"guards"`
	Created      time.Time         `json:"created"`
	StartBalance float64           `json:"start_balance"`
	Result       *plugins.Result   `json:"result,omitempty"`
	Extra        []plugins.Port    `json:"extra_ports,omitempty"`
	VRAM         int64             `json:"vram_mb"`
	GPUName      string            `json:"gpu_name"`
}

type stateFile struct {
	Accounts []accountRecord `json:"accounts"`
	Sessions []sessionRecord `json:"sessions"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
