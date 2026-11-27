package app

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"colaunch/internal/comfy"
	"colaunch/internal/core"
	"colaunch/internal/i18n"
)

// createInput belongs to one runtime and model family. Only the active
// form holds a selection. A running job can finish with its own snapshot.
type createInput struct {
	session, family string
	image           *comfy.InputImage
	preview         *ui.Bitmap
	denoise         float64
	err             string
}

func (cr *createState) selectSession(id string) {
	if cr.session != id {
		cr.session = id
		cr.input = nil
	}
}

func (cr *createState) inputFor(s *core.Session, f *comfy.Family) *createInput {
	if cr.session != s.ID {
		return nil
	}
	if !f.SupportsInputImage() {
		cr.input = nil
		return nil
	}
	if cr.input == nil || cr.input.session != s.ID || cr.input.family != f.ID {
		cr.input = &createInput{session: s.ID, family: f.ID, denoise: comfy.DefaultDenoise}
	}
	return cr.input
}

func (a *App) inputField(c *ui.Context, input *createInput, kind comfy.Kind, generating bool) {
	cr := &a.create
	t := c.Theme()
	label := i18n.T("create.input_image")
	if kind == kindVideo {
		label = i18n.T("create.input_first_frame")
	}
	ui.Field(c.Key("create-input"), label, func() {
		ui.Column(c).Gap(8).Children(func() {
			choose := i18n.T("create.input_choose")
			if input.image != nil {
				choose = i18n.T("create.input_replace")
				name := input.image.Filename
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Image(c, input.preview).Width(56).Height(56).Fit(ui.Contain).Radius(8).Label(label)
					ui.Text(c, name).FontSize(13).Grow(1).Shrink(1).SingleLine().Ellipsis("…").Tooltip(name)
				})
			} else {
				ui.Text(c, i18n.T("create.input_formats")).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
				if touchButton(c, labeledButton(c, iconImage, choose, false)).Disabled(generating || cr.inputPicker != nil).Clicked() {
					a.openCreateInput(input)
				}
				if input.image != nil && touchButton(c, labeledButton(c, iconTrash, i18n.T("create.input_remove"), false)).Disabled(generating || cr.inputPicker == input).Clicked() {
					input.image, input.preview, input.err = nil, nil, ""
				}
			})
			if cr.inputPicker == input {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, i18n.T("create.input_loading")).FontSize(12).TextColor(t.TextMuted)
				})
			}
			ui.Text(c, i18n.T("create.input_upload")).FontSize(12).TextColor(t.TextMuted)
		})
	}).Error(i18n.T(input.err))
	if input.image != nil && kind == kindImage {
		ui.Field(c.Key("create-denoise"), i18n.T("create.input_denoise"), func() {
			ui.NumberInput(c, &input.denoise, 0.1, 1, 0.05).Disabled(generating)
		}).Description(i18n.T("create.input_denoise_help"))
	}
}

func (a *App) openCreateInput(input *createInput) {
	cr := &a.create
	cr.inputPicker = input
	input.err = ""
	pick := cr.pickInputFiles
	if pick == nil {
		pick = mygo.Dialog.Open
	}
	opts := mygo.OpenDialogOptions{
		Parent: a.win,
		Title:  i18n.T("create.input_choose"),
		Filters: []mygo.FileFilter{{
			Name: "PNG, JPEG, WebP", Extensions: []string{"png", "jpg", "jpeg", "webp"},
			MIMETypes: []string{"image/png", "image/jpeg", "image/webp"},
		}},
	}
	go func() {
		paths, err := pick(opts)
		var image *comfy.InputImage
		var preview *ui.Bitmap
		var errKey string
		switch {
		case errors.Is(err, mygo.ErrCanceled):
		case err != nil:
			errKey = "create.input_picker_err"
		case len(paths) > 0:
			image, preview, err = readCreateInput(paths[0])
			if err != nil {
				errKey = "create.input_file_err"
			}
		}
		a.m.Post(func() {
			cr.inputPicker = nil
			if cr.input != input || cr.session != input.session {
				return
			}
			s := a.m.Session(input.session)
			if s == nil || s.Phase != core.PhaseRunning || !isComfy(s) || a.sessionFamily(s) != comfy.FamilyByID(input.family) {
				cr.input = nil
				return
			}
			input.err = errKey
			if image != nil {
				input.image, input.preview = image, preview
			}
		})
	}()
}

func readCreateInput(filename string) (*comfy.InputImage, *ui.Bitmap, error) {
	if !filepath.IsAbs(filename) {
		return nil, nil, errors.New("image picker did not return a local file")
	}
	// MyGo imports a temporary copy on iOS and Android. Its Android host
	// resolves the provider's content URI before returning this path.
	if mobileOS() {
		defer os.Remove(filename)
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	name := path.Base(strings.ReplaceAll(filepath.Base(filename), `\`, "/"))
	image, err := comfy.ReadInputImage(f, name)
	if err != nil {
		return nil, nil, err
	}
	preview, err := ui.DecodeBitmapScaled(image.Data, 192)
	if err != nil {
		return nil, nil, err
	}
	return &image, preview, nil
}
