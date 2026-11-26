package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
)

// knownError is an error people run into, as the interface explains it:
// what went wrong and what to do next, both i18n keys.
type knownError struct {
	key  string
	next string
	// match are lowercase pieces of the error's text, as core, colab,
	// google and hf write it, or as Colab's API words it.
	match []string
}

// knownErrors are tried in order: the first whose text matches explains
// the error.
var knownErrors = []knownError{
	{key: "err.no_client", next: "err.no_client_next", match: []string{"no oauth client"}},
	{key: "err.signed_out", next: "err.signed_out_next", match: []string{"sign-in expired", "was revoked", "refused the account's token", "invalid_grant", "unauthenticated"}},
	{key: "err.denylisted", next: "err.denylisted_next", match: []string{"blocked from colab", "denylisted"}},
	{key: "err.too_many", next: "err.too_many_next", match: []string{"too many runtimes", "too_many_active_runtimes"}},
	{key: "err.quota", next: "err.quota_next", match: []string{"not enough compute units", "usage quota", "quota_exceeded"}},
	{key: "err.unavailable", next: "err.unavailable_next", match: []string{"no such accelerator is available", "no accelerator is available"}},
	{key: "err.monitor_connection", next: "err.monitor_connection_next", match: []string{"opening a terminal to wait on", "colab: the terminal command didn't start"}},
	{key: "err.terminal_output", next: "err.terminal_output_next", match: []string{"colab: the output of command ", "invalid readiness probe response", "the readiness probe didn't answer"}},
	{key: "err.hf_not_found", next: "err.hf_not_found_next", match: []string{"hf: no such repository"}},
	{key: "err.too_big", next: "err.too_big_next", match: []string{"no quantization of the model fits"}},
	{key: "err.not_llm", next: "err.not_llm_next", match: []string{"llama.cpp can't serve"}},
	{key: "err.only_safetensors", next: "err.only_safetensors_next", match: []string{"safetensors weights but no gguf"}},
	{key: "err.only_projector", next: "err.only_projector_next", match: []string{"are only multimodal projectors"}},
	{key: "err.unnamed_gguf", next: "err.unnamed_gguf_next", match: []string{"gguf files names a quantization"}},
	{key: "err.no_gguf", next: "err.no_gguf_next", match: []string{"has no gguf files"}},
}

// explainError finds what an error's text means to the user: the i18n
// keys of its title and of the next step, or ok false for an error the
// interface doesn't know.
func explainError(msg string) (title, next string, ok bool) {
	low := strings.ToLower(msg)
	if low == "" {
		return "", "", false
	}
	for _, k := range knownErrors {
		for _, m := range k.match {
			if strings.Contains(low, m) {
				return k.key, k.next, true
			}
		}
	}
	return "", "", false
}

// shortError is an error in a few words, for a line: its title when the
// interface knows it, else the text itself.
func shortError(msg string) string {
	if title, _, ok := explainError(msg); ok {
		return i18n.T(title)
	}
	return msg
}

// errorNotice shows an error: what went wrong and what to do next, in the
// user's language, with Colab's own words under Details, which copy. An
// error the interface doesn't know shows as it is. id tells the notices
// apart, to remember which have their details open.
func (a *App) errorNotice(c *ui.Context, id, msg string) {
	if msg == "" {
		return
	}
	t := c.Theme()
	title, next, ok := explainError(msg)
	ui.Column(c.Key("err-"+id)).Gap(6).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
		if !ok {
			ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, msg).TextColor(t.Danger).FontSize(13).Selectable().Grow(1).Shrink(1)
				if iconButton(c, iconCopy, i18n.T("copy")).Clicked() {
					a.copyText(msg)
				}
			})
			return
		}
		ui.Text(c, i18n.T(title)).TextColor(t.Danger).Bold().FontSize(14)
		ui.Text(c, i18n.T(next)).FontSize(13)
		if a.details == nil {
			a.details = map[string]bool{}
		}
		open := a.details[id]
		ui.Collapsible(c, i18n.T("err.details"), &open, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
				ui.Text(c, msg).Font("monospace").FontSize(12).TextColor(t.TextMuted).Selectable().Grow(1).Shrink(1)
				if iconButton(c, iconCopy, i18n.T("copy")).Clicked() {
					a.copyText(msg)
				}
			})
		})
		a.details[id] = open
	})
}
