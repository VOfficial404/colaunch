package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

// createJob is a generation under way on a session.
type createJob struct {
	family   *comfy.Family
	params   comfy.Params
	client   *comfy.Client
	cancel   context.CancelFunc
	started  time.Time
	prog     comfy.Progress
	stopping bool
}

// ComfyUI's errors people meet, after the others: the first that matches
// explains an error.
func init() {
	knownErrors = append(knownErrors,
		knownError{key: "err.comfy_oom", next: "err.comfy_oom_next", match: []string{"out of memory", "outofmemory", "allocation on device"}},
		knownError{key: "err.comfy_node", next: "err.comfy_node_next", match: []string{"missing node", "missing_node_type", "node type not found", "because node", "unknown node"}},
		knownError{key: "err.comfy_model", next: "err.comfy_model_next", match: []string{"value not in list", "model file", "no such model", "model not found", "ckpt_name", "unet_name", "could not find model"}},
		knownError{key: "err.comfy_conn", next: "err.comfy_conn_next", match: []string{"connection refused", "connectex", "connection reset", "dial tcp", "no such host", "i/o timeout", "comfy: not ready", "unexpected eof"}},
	)
}

// suggestComfy tells whether an error, as a session keeps its text, is
// llama.cpp refusing a model the comfyui plugin runs, and the model to
// deploy with it: the one the error names, else repo, the one the
// deployment asked for. A model of a family the plugin doesn't know isn't
// suggested.
func suggestComfy(msg, repo string) (string, bool) {
	if msg == "" {
		return "", false
	}
	suggested, ok := plugins.SuggestComfy(errors.New(msg))
	if !ok {
		return "", false
	}
	if suggested == "" {
		suggested = repo
	}
	return suggested, true
}

// generate starts making what p asks for on s, in the background: it goes
// on when the user leaves the page.
func (a *App) generate(s *core.Session, f *comfy.Family, p comfy.Params) {
	cr := &a.create
	cr.init()
	if cr.jobs[s.ID] != nil {
		return
	}
	addr := a.comfyAddr(s)
	if addr == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &createJob{family: f, params: p, client: comfy.New("http://" + addr), cancel: cancel, started: time.Now()}
	cr.jobs[s.ID] = job
	delete(cr.errs, s.ID)
	id := s.ID
	dir, derr := cr.cacheDir()
	go func() {
		// Progress comes as fast as ComfyUI sends it; the window takes the
		// latest at most every createFrame.
		var mu sync.Mutex
		var latest comfy.Progress
		pending := false
		show := func() {
			mu.Lock()
			job.prog, pending = latest, false
			mu.Unlock()
		}
		outs, err := job.client.Generate(ctx, f, p, func(pr comfy.Progress) {
			mu.Lock()
			latest = pr
			later := !pending
			pending = true
			mu.Unlock()
			if later {
				time.AfterFunc(createFrame, func() { a.m.Post(show) })
			}
		})
		var made []*creation
		for i, o := range outs {
			it, perr := newCreation(o, i, dir, derr)
			if perr != nil {
				if err == nil {
					err = perr
				}
				continue
			}
			it.session, it.family, it.params = id, f, p
			made = append(made, it)
		}
		canceled := ctx.Err() != nil
		a.m.Post(func() {
			cancel()
			if cr.jobs[id] == job {
				delete(cr.jobs, id)
			}
			// Stopping ends the run in an error, which says nothing new,
			// as does a run stopped in ComfyUI's own page.
			if err != nil && !job.stopping && !canceled && !errors.Is(err, comfy.ErrInterrupted) {
				cr.errs[id] = err.Error()
			} else if err == nil && len(made) == 0 && !job.stopping {
				cr.errs[id] = i18n.T("create.no_output")
			}
			for i := len(made) - 1; i >= 0; i-- {
				cr.add(made[i])
			}
		})
	}()
}

// stopGenerate asks ComfyUI to stop what the session runs, then stops
// waiting for it.
func (a *App) stopGenerate(id string) {
	cr := &a.create
	job := cr.jobs[id]
	if job == nil || job.stopping {
		return
	}
	job.stopping = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := job.client.Interrupt(ctx)
		cancel()
		a.m.Post(func() {
			if err != nil {
				a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("create.err_stop"), Description: shortError(err.Error()), Type: "error"})
			}
			// What was queued and never ran doesn't wait on.
			job.cancel()
		})
	}()
}

// generating is how many generations are under way.
func (a *App) generating() int { return len(a.create.jobs) }

// cacheDir is the directory of the files the page makes, made the first
// time.
func (cr *createState) cacheDir() (string, error) {
	if cr.dir == "" {
		d, err := os.MkdirTemp("", "colaunch-create")
		if err != nil {
			return "", err
		}
		cr.dir = d
	}
	return cr.dir, os.MkdirAll(cr.dir, 0o700)
}

// cleanup closes the players and removes the files the page made, as the
// app quits.
func (cr *createState) cleanup() {
	for _, it := range cr.items {
		it.close()
	}
	cr.items, cr.playing = nil, nil
	if cr.dir != "" {
		os.RemoveAll(cr.dir)
	}
}

// add keeps a result, newest first, letting go of the oldest past
// createKeep.
func (cr *createState) add(it *creation) {
	cr.next++
	it.id = cr.next
	cr.items = append([]*creation{it}, cr.items...)
	for len(cr.items) > createKeep {
		old := cr.items[len(cr.items)-1]
		cr.items = cr.items[:len(cr.items)-1]
		cr.drop(old)
	}
}

// remove deletes a result and its file.
func (cr *createState) remove(it *creation) {
	for i, x := range cr.items {
		if x == it {
			cr.items = append(cr.items[:i], cr.items[i+1:]...)
			break
		}
	}
	cr.drop(it)
}

func (cr *createState) drop(it *creation) {
	if cr.playing == it {
		cr.playing = nil
	}
	it.close()
	if it.path != "" {
		os.Remove(it.path)
	}
}

// filename names a result to save: colaunch-<family>-<seed>[-n].<ext>.
func (it *creation) filename() string {
	name := "colaunch"
	if it.family != nil {
		name += "-" + it.family.ID
	}
	name += "-" + itoa(int(it.params.Seed))
	if it.index > 0 {
		name += "-" + itoa(it.index+1)
	}
	return name + it.ext
}

// extFor is the extension of an output's file.
func extFor(o comfy.Output) string {
	if e := strings.ToLower(filepath.Ext(o.Filename)); e != "" && len(e) <= 6 {
		return e
	}
	switch o.MIME {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	}
	if o.Kind == kindVideo {
		return ".mp4"
	}
	return ".png"
}
