package app

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// The Create page makes images and videos on runtimes that run ComfyUI.

const (
	kindImage = comfy.Kind("image")
	kindVideo = comfy.Kind("video")
	// comfyPlugin is the ID of the task that deploys ComfyUI, and
	// comfyRepo the key of its field naming the model on Hugging Face.
	comfyPlugin = "comfyui"
	comfyRepo   = "repo"
	// createKeep is how many results the page keeps, newest first.
	createKeep = 50
	// createFrame is how often progress is drawn while generating, at most.
	createFrame = 100 * time.Millisecond
)

// Icons from Lucide (lucide.dev), under the ISC license.
var (
	iconSparkles = lucide(`<path d="M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z"/><path d="M20 3v4"/><path d="M22 5h-4"/><path d="M4 17v2"/><path d="M5 18H3"/>`)
	iconImage    = lucide(`<rect width="18" height="18" x="3" y="3" rx="2" ry="2"/><circle cx="9" cy="9" r="2"/><path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21"/>`)
	iconDice     = lucide(`<rect width="12" height="12" x="2" y="10" rx="2" ry="2"/><path d="m17.92 14 3.5-3.5a2.24 2.24 0 0 0 0-3l-5-4.92a2.24 2.24 0 0 0-3 0L10 6"/><path d="M6 18h.01"/><path d="M10 14h.01"/><path d="M15 6h.01"/><path d="M18 9h.01"/>`)
	iconDownload = lucide(`<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" x2="12" y1="15" y2="3"/>`)
	iconPlay     = lucide(`<polygon points="6 3 20 12 6 21 6 3"/>`)
	iconPause    = lucide(`<rect x="14" y="4" width="4" height="16" rx="1"/><rect x="6" y="4" width="4" height="16" rx="1"/>`)
)

type createState struct {
	inited  bool
	session string
	input   *createInput
	// inputPicker is the form waiting for the single native picker or its read.
	inputPicker *createInput
	// pickInputFiles substitutes for the native dialog in UI tests.
	pickInputFiles func(mygo.OpenDialogOptions) ([]string, error)
	// forms are what the form says, by family.
	forms map[string]*createForm
	// picked is the family chosen for a session whose runtime didn't say.
	picked map[string]string
	// jobs are the generations running, and errs the last one's error, by
	// session.
	jobs map[string]*createJob
	errs map[string]string
	// items are the results, newest first.
	items []*creation
	next  int
	// dir holds the videos made, and what was saved or shared; it goes
	// when the app quits.
	dir string
	// playing is the video whose player is open: one at a time.
	playing      *creation
	confirmClear bool
	// addr, where not nil, says where a session's ComfyUI listens, for
	// tests.
	addr func(*core.Session) string
}

// createForm is the form of a family.
type createForm struct {
	prompt, negative string
	size             int
	steps, cfg       float64
	seed             string
	fixed            bool
	frames, fps      float64
	count            int // index: one to four images
	width, height    int // when the family has no sizes
	promptErr        string
}

func (cr *createState) init() {
	if cr.inited {
		return
	}
	cr.inited = true
	cr.forms = map[string]*createForm{}
	cr.picked = map[string]string{}
	cr.jobs = map[string]*createJob{}
	cr.errs = map[string]string{}
}

// isComfy reports whether a session serves ComfyUI.
func isComfy(s *core.Session) bool {
	if s.Result == nil {
		return false
	}
	for _, p := range s.Result.Ports {
		if p.Kind == plugins.PortComfy {
			return true
		}
	}
	return false
}

// comfySessions are the running sessions that serve ComfyUI.
func (a *App) comfySessions() []*core.Session {
	var out []*core.Session
	for _, s := range a.m.Live() {
		if s.Phase == core.PhaseRunning && isComfy(s) {
			out = append(out, s)
		}
	}
	return out
}

// comfyAddr is where a session's ComfyUI listens on this machine, as
// 127.0.0.1:8188, "" while it isn't forwarded.
func (a *App) comfyAddr(s *core.Session) string {
	if a.create.addr != nil {
		return a.create.addr(s)
	}
	for _, f := range s.Forwards {
		if f.Port.Kind == plugins.PortComfy && f.Err == "" {
			return f.Address()
		}
	}
	return ""
}

// sessionFamily is the family of models a session runs, as its runtime
// said or the user chose; nil when neither did.
func (a *App) sessionFamily(s *core.Session) *comfy.Family {
	if s.Result != nil {
		for _, kv := range s.Result.Info {
			if kv[0] == "family" {
				if f := comfy.FamilyByID(kv[1]); f != nil {
					return f
				}
			}
		}
	}
	return comfy.FamilyByID(a.create.picked[s.ID])
}

func familyName(f *comfy.Family) string {
	return plugins.Text(f.Name).In(i18n.Lang())
}

func kindName(k comfy.Kind) string {
	if k == kindVideo {
		return i18n.T("create.kind_video")
	}
	return i18n.T("create.kind_image")
}

// form is a family's form, made of its defaults the first time.
func (cr *createState) form(f *comfy.Family) *createForm {
	if fm := cr.forms[f.ID]; fm != nil {
		return fm
	}
	d := f.Defaults
	fm := &createForm{
		negative: d.Negative, steps: float64(d.Steps), cfg: d.CFG,
		frames: float64(d.Frames), fps: float64(d.FPS),
		width: d.Width, height: d.Height,
		count: min(max(d.Count, 1), 4) - 1,
		seed:  strconv.FormatInt(d.Seed, 10),
		fixed: d.Seed != 0,
	}
	if fm.steps <= 0 {
		fm.steps = 20
	}
	if fm.fps <= 0 {
		fm.fps = 16
	}
	if d.Seed == 0 {
		fm.seed = randomSeed()
	}
	for i, sz := range f.Sizes {
		if sz[0] == d.Width && sz[1] == d.Height {
			fm.size = i
		}
	}
	cr.forms[f.ID] = fm
	return fm
}

// load puts params in the form of their family.
func (cr *createState) load(f *comfy.Family, p comfy.Params) {
	fm := cr.form(f)
	fm.prompt, fm.negative = p.Prompt, p.Negative
	fm.steps, fm.cfg, fm.seed = float64(p.Steps), p.CFG, strconv.FormatInt(p.Seed, 10)
	fm.frames, fm.fps = float64(p.Frames), float64(p.FPS)
	fm.count = min(max(p.Count, 1), 4) - 1
	fm.width, fm.height = p.Width, p.Height
	for i, sz := range f.Sizes {
		if sz[0] == p.Width && sz[1] == p.Height {
			fm.size = i
		}
	}
}

// params are what the form asks for. Unless the seed is kept, each run
// takes a new one, which the form then shows.
func (fm *createForm) params(f *comfy.Family) comfy.Params {
	seed, err := strconv.ParseInt(strings.TrimSpace(fm.seed), 10, 64)
	if !fm.fixed || err != nil || seed < 0 {
		fm.seed = randomSeed()
		seed, _ = strconv.ParseInt(fm.seed, 10, 64)
	}
	p := comfy.Params{
		Prompt: strings.TrimSpace(fm.prompt),
		Steps:  int(fm.steps), CFG: fm.cfg, Seed: seed,
		Width: fm.width, Height: fm.height, Count: 1,
	}
	if f.Negative {
		p.Negative = strings.TrimSpace(fm.negative)
	}
	if fm.size < len(f.Sizes) {
		p.Width, p.Height = f.Sizes[fm.size][0], f.Sizes[fm.size][1]
	}
	if f.Kind == kindVideo {
		p.Frames, p.FPS = int(fm.frames), int(fm.fps)
	} else {
		p.Count = fm.count + 1
	}
	return p
}

// randomSeed is a new seed, as the form shows it.
func randomSeed() string { return strconv.FormatInt(rand.Int64N(1<<48), 10) }

// sizeLabel is a size as its aspect ratio and its pixels: "16:9 · 1344×768".
func sizeLabel(w, h int) string {
	return aspect(w, h) + " · " + fmt.Sprintf("%d×%d", w, h)
}

// aspect is the ratio of a size as people name it, the nearest of the
// usual ones where it is close, as 1216×832 is to 3:2.
func aspect(w, h int) string {
	if w <= 0 || h <= 0 {
		return "?"
	}
	r := float64(w) / float64(h)
	for _, x := range [][2]int{{1, 1}, {4, 3}, {3, 4}, {3, 2}, {2, 3}, {16, 9}, {9, 16}, {21, 9}, {9, 21}, {5, 4}, {4, 5}, {7, 4}, {4, 7}} {
		if math.Abs(r/(float64(x[0])/float64(x[1]))-1) < 0.03 {
			return fmt.Sprintf("%d:%d", x[0], x[1])
		}
	}
	g := gcd(w, h)
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func (a *App) createPage(c *ui.Context) {
	cr := &a.create
	cr.init()
	sessions := a.comfySessions()
	var s *core.Session
	for _, x := range sessions {
		if x.ID == cr.session {
			s = x
		}
	}
	if s == nil && len(sessions) > 0 {
		s = sessions[0]
		cr.selectSession(s.ID)
	}
	if s == nil {
		cr.input = nil
		pageScroll(c, i18n.T("nav.create"), nil, func() {
			a.createEmpty(c)
			if len(cr.items) > 0 {
				a.createResults(c, nil)
			}
		})
		a.clearDialog(c)
		return
	}
	w, _ := c.Size()
	if w < 1040 {
		pageScroll(c, i18n.T("nav.create"), nil, func() {
			a.sessionPicker(c, sessions, s)
			a.createForm(c, s)
			a.createResults(c, s)
		})
	} else {
		a.createWide(c, sessions, s)
	}
	a.clearDialog(c)
}

// createWide lays the form out beside the results, each scrolling.
func (a *App) createWide(c *ui.Context, sessions []*core.Session, s *core.Session) {
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(16, 24, 8, 24).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, i18n.T("nav.create")).FontSize(22).Bold()
			ui.Spacer(c)
			if len(sessions) > 1 {
				ui.Column(c).Width(320).Children(func() { a.sessionSelect(c, sessions, s) })
			} else {
				a.sessionLine(c, s)
			}
		})
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			ui.Scroll(c).Width(380).Children(func() {
				ui.Column(c).Padding(8, 12, 32, 24).Gap(16).Children(func() { a.createForm(c, s) })
			})
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(8, 24, 32, 12).Gap(16).Children(func() { a.createResults(c, s) })
			})
		})
	})
}

func (a *App) createEmpty(c *ui.Context) {
	t := c.Theme()
	card(c, func() {
		ui.Column(c).AlignItems(ui.Center).Gap(12).PaddingY(24).Children(func() {
			ui.Icon(c, iconImage).FontSize(40).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("create.none_title")).Bold().FontSize(16).TextAlign(ui.Center)
			ui.Text(c, i18n.T("create.none")).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(480)
			if labeledButton(c, iconRocket, i18n.T("create.deploy"), true).Clicked() {
				a.useComfy("")
			}
		})
	})
}

// sessionPicker chooses the runtime on narrow windows, where the title
// has no room for it.
func (a *App) sessionPicker(c *ui.Context, sessions []*core.Session, s *core.Session) {
	if len(sessions) > 1 {
		ui.Field(c, i18n.T("create.session"), func() { a.sessionSelect(c, sessions, s) })
		return
	}
	a.sessionLine(c, s)
}

func (a *App) sessionLine(c *ui.Context, s *core.Session) {
	t := c.Theme()
	ui.Text(c, shortTitle(s)+" · "+s.Spec.String()).FontSize(13).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(s.Title())
}

func (a *App) sessionSelect(c *ui.Context, sessions []*core.Session, s *core.Session) {
	names := make([]string, len(sessions))
	counts := make(map[string]int, len(sessions))
	for i, x := range sessions {
		names[i] = fmt.Sprintf("%s · %s", shortTitle(x), x.Spec)
		counts[names[i]]++
	}
	chosen := ""
	for i, x := range sessions {
		if counts[names[i]] > 1 {
			names[i] += " · " + x.ID
		}
		if x == s {
			chosen = names[i]
		}
	}
	if ui.Select(c, &chosen, names).Label(i18n.T("create.session")).Changed() {
		for i, n := range names {
			if n == chosen {
				a.create.selectSession(sessions[i].ID)
				return
			}
		}
	}
}

// createForm is what to make, and the button that makes it.
func (a *App) createForm(c *ui.Context, s *core.Session) {
	cr := &a.create
	t := c.Theme()
	f := a.sessionFamily(s)
	input := cr.inputFor(s, f)
	card(c, func() {
		if f == nil {
			a.familyPicker(c, s)
			return
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, familyName(f)).Bold().FontSize(15).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
			ui.Badge(c, kindName(f.Kind))
		})
		fm := cr.form(f)
		job := cr.jobs[s.ID]
		ui.Field(c, i18n.T("create.prompt"), func() {
			if ui.TextArea(c, &fm.prompt).Placeholder(i18n.T("create.prompt_ph")).Lines(3, 10).Changed() {
				fm.promptErr = ""
			}
		}).Error(fm.promptErr)
		if f.Negative {
			ui.Field(c, i18n.T("create.negative"), func() {
				ui.TextArea(c, &fm.negative).Placeholder(i18n.T("create.negative_ph")).Lines(2, 6)
			})
		}
		if input != nil {
			a.inputField(c, input, f.Kind, job != nil)
		}
		if len(f.Sizes) > 0 {
			labels := make([]string, len(f.Sizes))
			for i, sz := range f.Sizes {
				labels[i] = sizeLabel(sz[0], sz[1])
			}
			fm.size = min(fm.size, len(labels)-1)
			chosen := labels[fm.size]
			ui.Field(c, i18n.T("create.size"), func() {
				if ui.Select(c, &chosen, labels).Changed() {
					for i, l := range labels {
						if l == chosen {
							fm.size = i
						}
					}
				}
			})
		}
		cols := 2
		if w, _ := c.Size(); w < 360 {
			cols = 1
		}
		ui.Grid(c).Columns(cols).GapX(12).GapY(12).Children(func() {
			ui.Field(c, i18n.T("create.steps"), func() { ui.NumberInput(c, &fm.steps, 1, 150, 1) })
			ui.Field(c, i18n.T("create.cfg"), func() { ui.NumberInput(c, &fm.cfg, 0, 30, 0.5) })
			if f.Kind == kindVideo {
				ui.Field(c, i18n.T("create.frames"), func() { ui.NumberInput(c, &fm.frames, 1, 1000, 4) })
				ui.Field(c, i18n.T("create.fps"), func() { ui.NumberInput(c, &fm.fps, 1, 60, 1) })
			}
		})
		if f.Kind == kindVideo && fm.fps > 0 {
			ui.Text(c, i18n.F("create.duration", fm.frames/fm.fps)).FontSize(12).TextColor(t.TextMuted)
		}
		ui.Field(c, i18n.T("create.seed"), func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &fm.seed).Font("monospace").Autocorrect(false).Grow(1).Shrink(1).Label(i18n.T("create.seed"))
				if touchButton(c, iconButton(c, iconDice, i18n.T("create.seed_random"))).Clicked() {
					fm.seed = randomSeed()
				}
			})
		})
		switchRow(c, &fm.fixed, i18n.T("create.seed_fixed"))
		if !fm.fixed {
			ui.Text(c, i18n.T("create.seed_help")).FontSize(12).TextColor(t.TextMuted)
		}
		if f.Kind != kindVideo {
			ui.Field(c, i18n.T("create.count"), func() {
				ui.Segmented(c, &fm.count, "1", "2", "3", "4").Label(i18n.T("create.count"))
			})
		}
		addr := a.comfyAddr(s)
		if job != nil {
			a.createProgress(c, s, job)
		} else {
			if addr == "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, i18n.T("create.connecting")).FontSize(13).TextColor(t.TextMuted)
				})
			}
			opening := input != nil && cr.inputPicker == input
			b := labeledButton(c, iconSparkles, i18n.T("create.generate"), true).Disabled(addr == "" || opening)
			if isPhone(c) {
				b.MinHeight(44)
			}
			if b.Clicked() {
				if strings.TrimSpace(fm.prompt) == "" {
					fm.promptErr = i18n.T("create.need_prompt")
				} else {
					params := fm.params(f)
					var image *comfy.InputImage
					if input != nil && input.image != nil {
						image = input.image
						if f.Kind == kindImage {
							params.Denoise = input.denoise
						}
					}
					a.generate(s, f, params, image)
				}
			}
		}
		a.errorNotice(c, "create-"+s.ID, cr.errs[s.ID])
	})
}

// familyPicker asks which family a runtime runs, when it didn't say.
func (a *App) familyPicker(c *ui.Context, s *core.Session) {
	t := c.Theme()
	all := comfy.Families()
	names := make([]string, len(all))
	for i, f := range all {
		names[i] = familyName(f) + " · " + kindName(f.Kind)
	}
	chosen := ""
	ui.Text(c, i18n.T("create.family_unknown")).FontSize(13).TextColor(t.TextMuted)
	ui.Field(c, i18n.T("create.family"), func() {
		if ui.Select(c, &chosen, names).Changed() {
			for i, n := range names {
				if n == chosen {
					a.create.picked[s.ID] = all[i].ID
				}
			}
		}
	})
}

// createProgress shows a generation under way, and the button that stops
// it.
func (a *App) createProgress(c *ui.Context, s *core.Session, job *createJob) {
	t := c.Theme()
	p := job.prog
	value := -1.0
	if p.Steps > 0 {
		value = min(float64(p.Step)/float64(p.Steps), 1)
	}
	ui.Column(c.Key("progress")).Gap(6).Children(func() {
		ui.Progress(c, value).Label(i18n.T("create.generating"))
		var parts []string
		if p.Steps > 0 {
			parts = append(parts, i18n.F("create.step", p.Step, p.Steps))
		}
		if p.Queue > 0 {
			parts = append(parts, i18n.F("create.queue", p.Queue))
		}
		if p.Node != "" {
			parts = append(parts, i18n.F("create.node", p.Node))
		}
		if len(parts) == 0 {
			parts = append(parts, i18n.T("create.waiting"))
		}
		parts = append(parts, i18n.F("create.elapsed", duration(time.Since(job.started))))
		ui.Text(c, strings.Join(parts, " · ")).FontSize(12).TextColor(t.TextMuted)
		if p.Message != "" {
			ui.Text(c, p.Message).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Tooltip(p.Message)
		}
		label := i18n.T("stop")
		if job.stopping {
			label = i18n.T("create.stopping")
		}
		b := labeledButton(c, iconStop, label, false).Disabled(job.stopping)
		if isPhone(c) {
			b.MinHeight(44)
		}
		if b.Clicked() {
			a.stopGenerate(s.ID)
		}
	})
	// The time taken moves every second.
	c.After(time.Second)
}

// touchButton gives a button the size of a fingertip on phones.
func touchButton(c *ui.Context, b ui.Element) ui.Element {
	if isPhone(c) {
		b.MinWidth(44).MinHeight(44)
	}
	return b
}

// useComfy opens Deploy on the ComfyUI task, with repo as its model
// where it isn't "".
func (a *App) useComfy(repo string) {
	d := &a.deploy
	a.page = pageDeploy
	p := plugins.Get(comfyPlugin)
	if p == nil {
		return
	}
	d.form.Plugin = comfyPlugin
	if repo != "" {
		d.values(p)[comfyRepo] = repo
	}
	if variants[d.variant] == "cpu" {
		d.variant = 1
	}
	d.err, d.launched = "", ""
	clear(d.fieldErr)
}

// comfyNotice says what Colab allows of making images and videos, above
// the ComfyUI task's form.
func comfyNotice(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("comfy-notice")).Gap(6).Padding(10, 12).Radius(8).Border(1, t.Warning).Children(func() {
		ui.Text(c, i18n.T("create.notice_title")).Bold().FontSize(13)
		ui.Text(c, i18n.T("create.notice_text")).FontSize(13).TextColor(t.TextMuted)
		ui.Row(c).Children(func() {
			if labeledButton(c, iconExternal, i18n.T("free.faq"), false).Clicked() {
				openURL(colabFAQ)
			}
		})
	})
}

// comfySuggestion offers, under an error that says llama.cpp can't serve
// a model, to deploy it with ComfyUI instead. repo is the model the
// deployment asked for.
func (a *App) comfySuggestion(c *ui.Context, msg, repo string) {
	suggested, ok := suggestComfy(msg, repo)
	if !ok || plugins.Get(comfyPlugin) == nil {
		return
	}
	t := c.Theme()
	ui.Column(c.Key("use-comfy")).Gap(4).Children(func() {
		ui.Row(c).Children(func() {
			if labeledButton(c, iconSparkles, i18n.T("create.use_comfy"), true).Clicked() {
				a.useComfy(suggested)
			}
		})
		ui.Text(c, i18n.T("create.use_comfy_help")).FontSize(12).TextColor(t.TextMuted)
	})
}
