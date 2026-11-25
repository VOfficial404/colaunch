package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

// creation is a result: an image, kept in memory, or a video, kept as a
// file in the page's directory and played by one player at a time.
type creation struct {
	id      int
	session string
	family  *comfy.Family
	params  comfy.Params
	kind    comfy.Kind
	mime    string
	ext     string
	// index is its place among the outputs of its run.
	index int
	// data and bitmap are an image's; path is a video's file, or an
	// image's once saved or shared.
	data   []byte
	bitmap *ui.Bitmap
	path   string
	player *ui.VideoPlayer
	// still is a video's picture where it doesn't play, as animated WebP.
	still    *ui.Bitmap
	playErr  string
	shown    bool
	released bool
}

// newCreation makes a result of an output, on the goroutine that got it:
// it decodes an image, and writes a video to dir.
func newCreation(o comfy.Output, index int, dir string, dirErr error) (*creation, error) {
	it := &creation{kind: o.Kind, mime: o.MIME, ext: extFor(o), index: index}
	if it.kind == "" {
		it.kind = kindImage
		if strings.HasPrefix(o.MIME, "video/") {
			it.kind = kindVideo
		}
	}
	if it.kind == kindVideo {
		if dirErr != nil {
			return nil, dirErr
		}
		f, err := os.CreateTemp(dir, "video-*"+it.ext)
		if err != nil {
			return nil, err
		}
		_, err = f.Write(o.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
			return nil, err
		}
		it.path = f.Name()
		return it, nil
	}
	b, err := ui.DecodeBitmap(o.Data)
	if err != nil {
		return nil, fmt.Errorf("comfy: %s: %w", o.Filename, err)
	}
	it.data, it.bitmap = o.Data, b
	return it, nil
}

// aspectRatio is the result's width over its height.
func (it *creation) aspectRatio() float32 {
	if it.bitmap != nil {
		if w, h := ui.ImageSize(it.bitmap); w > 0 && h > 0 {
			return w / h
		}
	}
	if it.player != nil {
		if w, h := it.player.Size(); w > 0 && h > 0 {
			return float32(w) / float32(h)
		}
	}
	if it.params.Width > 0 && it.params.Height > 0 {
		return float32(it.params.Width) / float32(it.params.Height)
	}
	return 1
}

// open makes the player of a video, on the main thread.
func (it *creation) open() {
	if it.player != nil || it.still != nil || it.playErr != "" {
		return
	}
	var p *ui.VideoPlayer
	var err error
	switch it.mime {
	case "image/gif", "image/png", "image/apng", "image/webp":
		var data []byte
		data, err = os.ReadFile(it.path)
		if err != nil {
			break
		}
		var d ui.VideoDecoder
		switch it.mime {
		case "image/gif":
			d, err = ui.NewGIFDecoder(data)
		case "image/webp":
			// Animated WebP doesn't decode as a video: its first frame shows.
			it.still, err = ui.DecodeBitmap(data)
		default:
			d, err = ui.NewAPNGDecoder(data)
		}
		if err == nil && d != nil {
			p, err = ui.NewVideoPlayer(d)
		}
	default:
		p, err = video.Open(it.path)
	}
	if err != nil {
		it.playErr = err.Error()
		return
	}
	if p != nil {
		p.SetLoop(true)
		it.player = p
	}
}

func (it *creation) close() {
	if it.player != nil {
		it.player.Close()
		it.player = nil
	}
	it.still = nil
}

// play opens a video's player, closing the one open before.
func (cr *createState) play(it *creation) {
	if cr.playing != nil && cr.playing != it {
		cr.playing.close()
	}
	cr.playing = it
	it.open()
}

// createResults shows the results, newest first. s is the session the
// form runs on, nil for none.
func (a *App) createResults(c *ui.Context, s *core.Session) {
	cr := &a.create
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, i18n.T("create.results")).Bold().FontSize(15)
		if n := len(cr.items); n > 0 {
			ui.Badge(c, itoa(n))
		}
		ui.Spacer(c)
		if len(cr.items) > 0 {
			if touchButton(c, iconButton(c, iconBroom, i18n.T("create.clear"))).Clicked() {
				cr.confirmClear = true
			}
		}
	})
	if len(cr.items) == 0 {
		ui.Text(c, i18n.F("create.results_empty", createKeep)).FontSize(13).TextColor(t.TextMuted)
		return
	}
	cols := 1
	if !isPhone(c) {
		w, _ := c.Size()
		pane := w
		if w >= 1040 {
			pane = w - 220 - 380
		} else if w >= 720 {
			pane = w - 220
		}
		cols = max(1, min(4, int((pane-36)/210)))
	}
	ui.Grid(c).Columns(cols).GapX(12).GapY(12).Children(func() {
		for _, it := range cr.items {
			ui.Column(c.Key(fmt.Sprintf("item-%d", it.id))).Children(func() { a.creationCard(c, s, it) })
		}
	})
}

func (a *App) creationCard(c *ui.Context, s *core.Session, it *creation) {
	cr := &a.create
	t := c.Theme()
	ui.Column(c).Gap(8).Padding(10).Radius(12).Background(t.Surface).Border(1, t.Border).Children(func() {
		a.creationMedia(c, it)
		p := it.params
		meta := i18n.F("create.meta", p.Width, p.Height, p.Steps, p.CFG, p.Seed)
		if it.kind == kindVideo {
			meta += " · " + i18n.F("create.meta_video", p.Frames, p.FPS)
		}
		if it.family != nil {
			meta = familyName(it.family) + " · " + meta
		}
		ui.Text(c, meta).FontSize(12).TextColor(t.TextMuted)
		if p.Prompt != "" {
			ui.Text(c, p.Prompt).FontSize(13).MaxLines(3).Ellipsis("…").Tooltip(p.Prompt)
		}
		ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
			if touchButton(c, iconButton(c, iconDownload, i18n.T("save"))).Clicked() {
				a.exportCreation(it, false)
			}
			if mobileOS() {
				if touchButton(c, iconButton(c, iconShare, i18n.T("create.share"))).Clicked() {
					a.exportCreation(it, true)
				}
			}
			if touchButton(c, iconButton(c, iconCopy, i18n.T("create.copy_prompt"))).Clicked() {
				a.copyText(p.Prompt)
			}
			if touchButton(c, iconButton(c, iconRefresh, i18n.T("create.again"))).Clicked() {
				a.again(s, it)
			}
			ui.Spacer(c)
			if touchButton(c, iconButton(c, iconTrash, i18n.T("create.delete"))).Clicked() {
				cr.remove(it)
			}
		})
	})
}

// creationMedia shows the image, or the video with a button that plays
// and pauses it.
func (a *App) creationMedia(c *ui.Context, it *creation) {
	cr := &a.create
	t := c.Theme()
	ratio := it.aspectRatio()
	if it.bitmap != nil {
		ui.Image(c, it.bitmap).FillWidth().AspectRatio(ratio).Fit(ui.Contain).Radius(8).Label(it.params.Prompt)
		// Once shown, the GPU has it: the CPU's copy can go.
		if it.shown && !it.released {
			it.bitmap.ReleasePixels()
			it.released = true
		}
		it.shown = true
		return
	}
	if cr.playing == it && it.player != nil {
		p := it.player
		ui.Video(c, p).FillWidth().AspectRatio(ratio).Fit(ui.Contain).Radius(8).Label(it.params.Prompt)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			icon, label := iconPause, i18n.T("create.pause")
			if !p.Playing() {
				icon, label = iconPlay, i18n.T("create.play")
			}
			if touchButton(c, iconButton(c, icon, label)).Clicked() {
				if p.Playing() {
					p.Pause()
				} else {
					p.Play()
				}
			}
			if !p.Ready() {
				ui.Spinner(c)
			}
		})
		if err := p.Err(); err != nil {
			ui.Text(c, i18n.T("create.video_err")).FontSize(12).TextColor(t.Danger)
		}
		return
	}
	if cr.playing == it && it.still != nil {
		ui.Image(c, it.still).FillWidth().AspectRatio(ratio).Fit(ui.Contain).Radius(8)
		return
	}
	box := ui.Column(c).FillWidth().AspectRatio(ratio).Radius(8).Background(t.Background).Border(1, t.Border).Center().Gap(8).
		Focusable().Role(ui.RoleButton).Label(i18n.T("create.play"))
	box.Children(func() {
		ui.Icon(c, iconPlay).FontSize(32).TextColor(accent)
		if it.playErr != "" {
			ui.Text(c, i18n.T("create.video_err")).FontSize(12).TextColor(t.Danger).TextAlign(ui.Center).PaddingX(12)
		}
	})
	if box.Clicked() {
		cr.play(it)
	}
}

// again makes a result again with its settings, on the runtime that made
// it while it runs, else on the one the form runs on.
func (a *App) again(s *core.Session, it *creation) {
	cr := &a.create
	target := s
	for _, x := range a.comfySessions() {
		if x.ID == it.session {
			target = x
		}
	}
	if target == nil || it.family == nil {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.gone"), Type: "error"})
		return
	}
	if f := a.sessionFamily(target); f == nil || f.ID != it.family.ID {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.gone"), Type: "error"})
		return
	}
	if cr.jobs[target.ID] != nil {
		a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.busy"), Type: "info", Timeout: 2e9})
		return
	}
	cr.session = target.ID
	cr.load(it.family, it.params)
	cr.form(it.family).fixed = true
	a.generate(target, it.family, it.params)
}

func mobileOS() bool { return runtime.GOOS == "android" || runtime.GOOS == "ios" }

// exportCreation saves a result where the user chooses, through the save
// dialog on desktops and the system's picker on phones, or shares it.
func (a *App) exportCreation(it *creation, share bool) {
	cr := &a.create
	if it.path == "" {
		dir, err := cr.cacheDir()
		if err == nil {
			path := filepath.Join(dir, fmt.Sprintf("%d-%s", it.id, it.filename()))
			if err = os.WriteFile(path, it.data, 0o600); err == nil {
				it.path = path
			}
		}
		if err != nil {
			a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.err_save"), Description: err.Error(), Type: "error"})
			return
		}
	}
	path, name := it.path, it.filename()
	go func() {
		if share {
			err := mygo.Share(mygo.ShareOptions{Files: []string{path}})
			if err != nil {
				a.m.Post(func() {
					a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.err_share"), Description: err.Error(), Type: "error"})
				})
			}
			return
		}
		to, err := mygo.Dialog.SaveCopy(path, name)
		a.m.Post(func() {
			switch {
			case err != nil && !errors.Is(err, mygo.ErrCanceled):
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.err_save"), Description: err.Error(), Type: "error"})
			case err == nil && to != "":
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.F("create.saved", filepath.Base(to)), Type: "success", Timeout: 2e9})
			}
		})
	}()
}

// clearDialog asks before clearing every result.
func (a *App) clearDialog(c *ui.Context) {
	cr := &a.create
	if !cr.confirmClear {
		return
	}
	if dangerConfirm(c, &cr.confirmClear, i18n.T("create.clear_title"), i18n.T("create.clear_text"), i18n.T("cancel"), i18n.T("create.clear_confirm")) == 1 {
		for _, it := range cr.items {
			cr.drop(it)
		}
		cr.items = nil
	}
}
