package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"colaunch/internal/i18n"
	"colaunch/internal/plugins"
)

var (
	editVariants   = []string{"", "VARIANT_CPU", "VARIANT_GPU", "VARIANT_TPU"}
	editFieldKinds = []plugins.FieldKind{plugins.FieldText, plugins.FieldPassword, plugins.FieldNumber, plugins.FieldSelect, plugins.FieldSwitch}
	editPortKinds  = []plugins.PortKind{plugins.PortWeb, plugins.PortOpenAI, plugins.PortTCP}
)

// newTaskScript is the script a new task starts with.
const newTaskScript = `# Runs on the runtime with bash. Fields arrive as COLAUNCH_<KEY>.
set -euo pipefail
# Installing serves nothing: keep the idle watchdog away meanwhile.
( while true; do touch "$COLAUNCH_BUSY"; sleep 30; done ) &
BUSY=$!
echo "@@PHASE install@@"
# python3 -m pip install -q your-package
kill "$BUSY"
echo "@@PHASE start@@"
exec python3 -m http.server 8000 --bind 127.0.0.1
`

// taskEditor is a task as the editor edits it.
type taskEditor struct {
	// orig is the task edited, nil for a new one or a copy.
	orig   *plugins.ScriptPlugin
	names  [3]string
	descs  [3]string
	id     string
	idSet  bool
	hw     int
	hf     bool
	fields []*editField
	ports  []*editPort
	ready  string
	// timeout is in minutes, 0 for the default.
	timeout float64
	script  string
	// What the editor doesn't show, kept as it was.
	model, modelField, author, readme string
	next                              int

	err, errDetail string
}

type editField struct {
	n                  int
	key, label, def    string
	labelWas           string
	options            string
	kind               int
	required, advanced bool
	orig               plugins.Field
}

type editPort struct {
	n             int
	name, path    string
	remote, local float64
	kind          int
}

func indexOf[T comparable](list []T, v T) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}

// newTaskEditor edits sp, or makes a new task when it's nil; with copy,
// a new task that starts as sp.
func newTaskEditor(sp *plugins.ScriptPlugin, copy bool) *taskEditor {
	e := &taskEditor{}
	if sp == nil {
		e.script = newTaskScript
		e.ports = []*editPort{{n: 1, name: "Web", remote: 8000, local: 8000, kind: 0, path: "/"}}
		e.next = 2
		return e
	}
	if !copy {
		e.orig = sp
	}
	m := sp.Manifest
	for i, lang := range i18n.Languages {
		e.names[i], e.descs[i] = m.Name[lang], m.Description[lang]
	}
	if strings.Join(e.names[:], "") == "" {
		e.names[0] = m.Name.In("en")
	}
	if strings.Join(e.descs[:], "") == "" {
		e.descs[0] = m.Description.In("en")
	}
	e.id, e.idSet = m.ID, true
	if copy {
		e.id += "-copy"
	}
	e.hw = indexOf(editVariants, m.Variant)
	e.hf = m.HFToken
	lang := i18n.Lang()
	for _, f := range m.Fields {
		e.next++
		label := f.Label.In(lang)
		e.fields = append(e.fields, &editField{
			n: e.next, key: f.Key, label: label, labelWas: label, def: f.Default,
			options: strings.Join(f.Options, ", "), kind: indexOf(editFieldKinds, f.Kind),
			required: f.Required, advanced: f.Advanced, orig: f,
		})
	}
	for _, p := range m.Ports {
		e.next++
		e.ports = append(e.ports, &editPort{n: e.next, name: p.Name, path: p.Path, remote: float64(p.Remote), local: float64(p.Local), kind: indexOf(editPortKinds, p.Kind)})
	}
	e.ready, e.timeout, e.script = m.Ready, m.Timeout, sp.Script()
	e.model, e.modelField, e.author, e.readme = m.Model, m.ModelField, sp.Meta.Author, sp.Readme
	return e
}

// manifest is the task as edited.
func (e *taskEditor) manifest() plugins.Manifest {
	m := plugins.Manifest{
		ID: strings.TrimSpace(e.id), Name: plugins.Text{}, Description: plugins.Text{},
		Variant: editVariants[e.hw], HFToken: e.hf,
		Ready: strings.TrimSpace(e.ready), Timeout: e.timeout, Model: e.model,
	}
	for i, lang := range i18n.Languages {
		if s := strings.TrimSpace(e.names[i]); s != "" {
			m.Name[lang] = s
		}
		if s := strings.TrimSpace(e.descs[i]); s != "" {
			m.Description[lang] = s
		}
	}
	lang := i18n.Lang()
	for _, f := range e.fields {
		out := f.orig
		out.Key = strings.TrimSpace(f.key)
		out.Kind = editFieldKinds[f.kind]
		out.Default = strings.TrimSpace(f.def)
		out.Required, out.Advanced = f.required, f.advanced
		out.Label = plugins.Text{}
		for k, v := range f.orig.Label {
			out.Label[k] = v
		}
		if label := strings.TrimSpace(f.label); label != strings.TrimSpace(f.labelWas) || len(out.Label) == 0 {
			if label == "" {
				delete(out.Label, lang)
			} else {
				out.Label[lang] = label
			}
		}
		if len(out.Label) == 0 {
			out.Label = plugins.Text{"en": out.Key}
		}
		out.Options = nil
		if out.Kind == plugins.FieldSelect {
			for _, o := range strings.Split(f.options, ",") {
				if o = strings.TrimSpace(o); o != "" {
					out.Options = append(out.Options, o)
				}
			}
		}
		m.Fields = append(m.Fields, out)
		if out.Key == e.modelField {
			m.ModelField = e.modelField
		}
	}
	for _, p := range e.ports {
		m.Ports = append(m.Ports, plugins.Port{
			Name: strings.TrimSpace(p.name), Remote: int(p.remote), Local: int(p.local),
			Kind: editPortKinds[p.kind], Path: strings.TrimSpace(p.path),
		})
	}
	return m
}

func (e *taskEditor) firstName() string {
	for _, n := range e.names {
		if strings.TrimSpace(n) != "" {
			return n
		}
	}
	return ""
}

func (a *App) taskEditorPage(c *ui.Context) {
	s := &a.tasks
	e := s.ed
	t := c.Theme()
	title := i18n.T("tasks.new_title")
	if e.orig != nil {
		title = i18n.T("tasks.edit_title")
	}
	cols := formCols(c)
	a.taskFrame(c, title, func() {
		section(c, i18n.T("tasks.about"), func() {
			ui.Grid(c).Columns(cols).GapX(16).GapY(12).Children(func() {
				for i, lang := range i18n.Languages {
					ui.Field(c.Key("name-"+lang), i18n.T("tasks.name")+" · "+i18n.Autonym(lang), func() {
						if ui.TextInput(c, &e.names[i]).Changed() && !e.idSet {
							e.id = plugins.SuggestID(e.firstName())
						}
					})
				}
			})
			ui.Text(c, i18n.T("tasks.name_help")).FontSize(12).TextColor(t.TextMuted)
			for i, lang := range i18n.Languages {
				ui.Field(c.Key("desc-"+lang), i18n.T("tasks.description")+" · "+i18n.Autonym(lang), func() {
					ui.TextArea(c, &e.descs[i]).Lines(1, 4)
				})
			}
			ui.Field(c, fieldLabel(i18n.T("tasks.id"), true), func() {
				if ui.TextInput(c, &e.id).Font("monospace").Autocorrect(false).Capitalize(ui.CapitalizeNone).Changed() {
					e.idSet = true
				}
			})
			ui.Text(c, i18n.T("tasks.id_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, i18n.T("tasks.hardware"), func() {
				ui.Segmented(c, &e.hw, i18n.T("tasks.hw_any"), "CPU", "GPU", "TPU")
			})
			switchRow(c, &e.hf, i18n.T("tasks.needs_hf"))
		})

		section(c, i18n.T("tasks.fields"), func() {
			ui.Text(c, i18n.T("tasks.fields_help")).FontSize(12).TextColor(t.TextMuted)
			kinds := []string{i18n.T("tasks.kind_text"), i18n.T("tasks.kind_password"), i18n.T("tasks.kind_number"), i18n.T("tasks.kind_select"), i18n.T("tasks.kind_switch")}
			for i := 0; i < len(e.fields); i++ {
				f := e.fields[i]
				removed := false
				ui.Column(c.Key(fmt.Sprint("field-", f.n))).Gap(10).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
					ui.Grid(c).Columns(cols).GapX(16).GapY(10).Children(func() {
						ui.Field(c, fieldLabel(i18n.T("tasks.field_key"), true), func() {
							ui.TextInput(c, &f.key).Font("monospace").Placeholder(example("model")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
						})
						ui.Field(c, i18n.T("tasks.field_label"), func() { ui.TextInput(c, &f.label) })
						ui.Field(c, i18n.T("tasks.field_kind"), func() {
							cur := kinds[f.kind]
							if ui.Select(c, &cur, kinds).Changed() {
								f.kind = indexOf(kinds, cur)
							}
						})
						ui.Field(c, i18n.T("tasks.field_default"), func() {
							ui.TextInput(c, &f.def).Autocorrect(false).Capitalize(ui.CapitalizeNone)
						})
					})
					if editFieldKinds[f.kind] == plugins.FieldSelect {
						ui.Field(c, i18n.T("tasks.field_options"), func() {
							ui.TextInput(c, &f.options).Placeholder(example("a, b, c")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
						})
					}
					ui.Row(c).Gap(16).Wrap().AlignItems(ui.Center).Children(func() {
						switchRow(c, &f.required, i18n.T("tasks.field_required"))
						switchRow(c, &f.advanced, i18n.T("tasks.field_advanced"))
						ui.Spacer(c)
						if labeledButton(c, iconTrash, i18n.T("tasks.remove"), false).Clicked() {
							removed = true
						}
					})
				})
				if removed {
					e.fields = append(e.fields[:i], e.fields[i+1:]...)
					i--
				}
			}
			ui.Row(c).Children(func() {
				if labeledButton(c, iconPlus, i18n.T("tasks.add_field"), false).Clicked() {
					e.next++
					e.fields = append(e.fields, &editField{n: e.next})
				}
			})
		})

		section(c, i18n.T("tasks.ports"), func() {
			ui.Text(c, i18n.T("tasks.ports_help")).FontSize(12).TextColor(t.TextMuted)
			kinds := []string{i18n.T("tasks.kind_web"), i18n.T("tasks.kind_openai"), i18n.T("tasks.kind_tcp")}
			for i := 0; i < len(e.ports); i++ {
				p := e.ports[i]
				removed := false
				ui.Column(c.Key(fmt.Sprint("port-", p.n))).Gap(10).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
					ui.Grid(c).Columns(cols).GapX(16).GapY(10).Children(func() {
						ui.Field(c, i18n.T("tasks.port_name"), func() { ui.TextInput(c, &p.name) })
						ui.Field(c, i18n.T("tasks.port_kind"), func() {
							cur := kinds[p.kind]
							if ui.Select(c, &cur, kinds).Changed() {
								p.kind = indexOf(kinds, cur)
							}
						})
						ui.Field(c, i18n.T("tasks.port_remote"), func() {
							if ui.NumberInput(c, &p.remote, 0, 65535, 1).Changed() && p.local == 0 {
								p.local = p.remote
							}
						})
						ui.Field(c, i18n.T("tasks.port_local"), func() { ui.NumberInput(c, &p.local, 0, 65535, 1) })
						ui.Field(c, i18n.T("tasks.port_path"), func() {
							ui.TextInput(c, &p.path).Font("monospace").Placeholder(example("/")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
						})
					})
					ui.Row(c).Children(func() {
						ui.Spacer(c)
						if labeledButton(c, iconTrash, i18n.T("tasks.remove"), false).Clicked() {
							removed = true
						}
					})
				})
				if removed {
					e.ports = append(e.ports[:i], e.ports[i+1:]...)
					i--
				}
			}
			ui.Row(c).Children(func() {
				if labeledButton(c, iconPlus, i18n.T("tasks.add_port"), false).Clicked() {
					e.next++
					e.ports = append(e.ports, &editPort{n: e.next, kind: 0, path: "/"})
				}
			})
		})

		section(c, i18n.T("tasks.start"), func() {
			ui.Field(c, i18n.T("tasks.ready"), func() {
				ui.TextInput(c, &e.ready).Font("monospace").Placeholder(example("curl -sf -o /dev/null http://127.0.0.1:8000/")).Autocorrect(false).Capitalize(ui.CapitalizeNone)
			})
			ui.Text(c, i18n.T("tasks.ready_help")).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, i18n.T("tasks.timeout"), func() {
				ui.NumberInput(c, &e.timeout, 0, 1440, 5).Width(160)
			})
		})

		section(c, i18n.T("tasks.script"), func() {
			ui.Text(c, i18n.T("tasks.script_help")).FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c, &e.script).Font("monospace").FontSize(13).Lines(12, 30).Autocorrect(false).Capitalize(ui.CapitalizeNone)
		})

		if e.err != "" {
			ui.Column(c).Gap(4).Padding(10, 12).Radius(8).Border(1, t.Danger).Children(func() {
				ui.Text(c, e.err).FontSize(13).TextColor(t.Danger)
				if e.errDetail != "" {
					ui.Text(c, e.errDetail).Font("monospace").FontSize(12).TextColor(t.TextMuted).Selectable()
				}
			})
		}
	}, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if ui.Button(c, i18n.T("cancel")).Clicked() {
				s.ed = nil
				s.view = taskList
			}
			if labeledButton(c, iconCheck, i18n.T("save"), true).Clicked() {
				a.saveTask()
			}
		})
	})
}

// saveTask writes the task of the editor into the plugins folder and
// loads the folder again.
func (a *App) saveTask() {
	s := &a.tasks
	e := s.ed
	if e.orig != nil && a.taskInUse(e.orig.ID()) {
		e.err, e.errDetail = i18n.T("tasks.in_use"), ""
		return
	}
	pack := plugins.NewPack(e.manifest(), e.script)
	pack.Author, pack.Readme = e.author, e.readme
	sp, err := plugins.Save(pack, a.m.PluginsDir(), e.orig)
	if err != nil {
		e.err, e.errDetail = i18n.T("tasks.err_save"), strings.TrimPrefix(err.Error(), plugins.ErrInvalid.Error()+": ")
		return
	}
	a.m.ReloadPlugins()
	s.ed = nil
	s.view = taskList
	a.toasts = append(a.toasts, ui.Toast{Title: i18n.T("tasks.saved"), Description: sp.Name().In(i18n.Lang()), Type: "success", Timeout: 2e9})
}
