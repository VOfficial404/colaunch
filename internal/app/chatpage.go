package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"colaunch/internal/chat"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

type chatState struct {
	session string
	convs   map[string]*conversation
	input   string
	list    ui.ListState
	md      markdown.Options
	system  string
	temp    float64
	inited  bool
	showSys bool
}

type conversation struct {
	msgs      []*message
	next      int
	streaming bool
	cancel    context.CancelFunc
	stats     string
}

type message struct {
	id        int
	role      string
	text      string
	reasoning string
	done      bool
	err       string
	showThink bool
}

func (a *App) chatPage(c *ui.Context) {
	ch := &a.chat
	if !ch.inited {
		ch.convs, ch.temp, ch.inited = map[string]*conversation{}, 0.7, true
	}
	t := c.Theme()
	var sessions []*core.Session
	for _, s := range a.m.Live() {
		if s.Phase == core.PhaseRunning && s.OpenAI() != "" {
			sessions = append(sessions, s)
		}
	}
	s := a.m.Session(ch.session)
	if s == nil || s.Ended() || s.OpenAI() == "" {
		s = nil
		if len(sessions) > 0 {
			s = sessions[0]
			ch.session = s.ID
		}
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(12, 20).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Wrap().Children(func() {
			ui.Text(c, i18n.T("nav.chat")).FontSize(18).Bold()
			if len(sessions) > 1 {
				names := make([]string, len(sessions))
				chosen := ""
				for i, x := range sessions {
					names[i] = fmt.Sprintf("%s · %s", x.Title(), x.Spec)
					if x == s {
						chosen = names[i]
					}
				}
				if ui.Select(c, &chosen, names).Changed() {
					for i, n := range names {
						if n == chosen {
							ch.session = sessions[i].ID
						}
					}
				}
			} else if s != nil {
				ui.Text(c, s.Title()+" · "+s.Spec.String()).TextColor(t.TextMuted).FontSize(13).SingleLine().Ellipsis("…").Shrink(1)
			}
			ui.Spacer(c)
			if s != nil {
				if ui.Button(c, i18n.T("chat.system")).Clicked() {
					ch.showSys = !ch.showSys
				}
				if iconButton(c, iconBroom, i18n.T("chat.clear")).Clicked() {
					if cv := ch.convs[s.ID]; cv != nil && cv.cancel != nil {
						cv.cancel()
					}
					delete(ch.convs, s.ID)
				}
			}
		})
		if s == nil {
			ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
				ui.Icon(c, iconChat).FontSize(40).TextColor(t.TextMuted)
				ui.Text(c, i18n.T("chat.none")).TextColor(t.TextMuted).TextAlign(ui.Center)
				if labeledButton(c, iconRocket, i18n.T("nav.deploy"), true).Clicked() {
					a.page = pageDeploy
				}
			})
			return
		}
		if ch.showSys {
			ui.Column(c).Padding(8, 20).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.TextArea(c, &ch.system).Placeholder(i18n.T("chat.system_ph")).Lines(2, 6)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, i18n.F("chat.temp", ch.temp)).FontSize(12).Width(140)
					ui.Slider(c, &ch.temp, 0, 2).Step(0.05).Grow(1)
				})
			})
		}
		cv := ch.convs[s.ID]
		if cv == nil {
			cv = &conversation{}
			ch.convs[s.ID] = cv
		}
		ch.list.Key = func(i int) any { return cv.msgs[i].id }
		ch.list.FollowEnd = true
		ch.list.FollowAnimation = &ui.Smooth
		ui.List(c, &ch.list, len(cv.msgs), func(i int) {
			a.chatMessage(c, cv.msgs[i])
		}).Grow(1).Padding(16, 20).Gap(16).Justify(ui.End).Children(func() {
			if len(cv.msgs) == 0 {
				ui.Text(c, i18n.F("chat.empty", s.OpenAI())).TextColor(t.TextMuted).Margin(ui.Auto).TextAlign(ui.Center)
			}
		})
		if cv.stats != "" {
			ui.Text(c, cv.stats).FontSize(11).TextColor(t.TextMuted).PaddingX(20)
		}
		ui.Row(c).Gap(8).Padding(12, 20).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).AlignItems(ui.End).Children(func() {
			in := ui.TextArea(c, &ch.input).Placeholder(i18n.T("chat.placeholder")).Lines(1, 6).Grow(1).AutoFocus()
			send := in.Submitted()
			if cv.streaming {
				if labeledButton(c, iconStop, i18n.T("stop"), false).Clicked() && cv.cancel != nil {
					cv.cancel()
				}
			} else if labeledButton(c, iconSend, i18n.T("chat.send"), true).Disabled(strings.TrimSpace(ch.input) == "").Clicked() || send {
				if strings.TrimSpace(ch.input) != "" {
					a.send(s, cv)
				}
			}
		})
	})
}

func (a *App) chatMessage(c *ui.Context, m *message) {
	t := c.Theme()
	if m.role == "user" {
		ui.Row(c).Children(func() {
			ui.Spacer(c)
			ui.Text(c, m.text).Selectable().Padding(8, 12).Radius(14).Background(t.Surface).MaxWidth(560)
		})
		return
	}
	ui.Column(c).MaxWidth(780).Gap(6).Children(func() {
		if m.reasoning != "" {
			label := i18n.T("chat.thinking")
			if m.done || m.text != "" {
				label = i18n.T("chat.thought")
			}
			if ui.Button(c, label).Clicked() {
				m.showThink = !m.showThink
			}
			if m.showThink || (m.text == "" && !m.done) {
				ui.Text(c, m.reasoning).FontSize(13).TextColor(t.TextMuted).Selectable().PaddingStart(12).BorderWidth(0, 0, 0, 2).BorderColor(t.Border)
			}
		}
		if m.done {
			markdown.View(c, m.text, &a.chat.md)
		} else if m.text != "" {
			markdown.Stream(c, m.text, &a.chat.md)
		} else if m.reasoning == "" {
			ui.Spinner(c)
		}
		if m.err != "" {
			ui.Text(c, m.err).TextColor(t.Danger).FontSize(13).Selectable()
		}
	})
}

// chatFrame is how often a streaming reply is drawn, about 30 times a
// second.
const chatFrame = 33 * time.Millisecond

func (a *App) send(s *core.Session, cv *conversation) {
	ch := &a.chat
	cv.next++
	cv.msgs = append(cv.msgs, &message{id: cv.next, role: "user", text: strings.TrimSpace(ch.input), done: true})
	ch.input = ""
	var history []chat.Message
	if sys := strings.TrimSpace(ch.system); sys != "" {
		history = append(history, chat.Message{Role: "system", Content: sys})
	}
	for _, m := range cv.msgs {
		if m.err == "" {
			history = append(history, chat.Message{Role: m.role, Content: m.text})
		}
	}
	cv.next++
	reply := &message{id: cv.next, role: "assistant"}
	cv.msgs = append(cv.msgs, reply)
	cv.streaming = true
	ctx, cancel := context.WithCancel(context.Background())
	cv.cancel = cancel
	base, model, key, temp := s.OpenAI(), "", "", ch.temp
	if s.Result != nil {
		model, key = s.Result.Model, s.Result.APIKey
	}
	go func() {
		// The reply grows in builders, which the window reads at most
		// every chatFrame: one redraw for the tokens that came meanwhile.
		var mu sync.Mutex
		var text, thought strings.Builder
		pending := false
		show := func() {
			mu.Lock()
			reply.text, reply.reasoning = text.String(), thought.String()
			pending = false
			mu.Unlock()
		}
		stats, err := chat.Stream(ctx, base, key, model, history, temp, func(content, reasoning string) {
			mu.Lock()
			text.WriteString(content)
			thought.WriteString(reasoning)
			later := !pending
			pending = true
			mu.Unlock()
			if later {
				time.AfterFunc(chatFrame, func() { a.win.Update(show) })
			}
		})
		a.win.Update(func() {
			show()
			reply.done = true
			cv.streaming = false
			cv.cancel = nil
			if err != nil && ctx.Err() == nil {
				reply.err = err.Error()
			}
			if stats != nil && stats.CompletionTokens > 0 {
				cv.stats = i18n.F("chat.stats", stats.PromptTokens, stats.CompletionTokens, stats.TokensPerSecond)
			}
		})
	}()
}
