package comfy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"colaunch/internal/hf"
)

// ErrUnknownFamily is returned for a model none of the families is.
var ErrUnknownFamily = errors.New("comfy: not a model family the comfyui plugin knows")

// unsupported are models people will try that aren't any family's yet,
// with why, so that they're told rather than guessed at.
var unsupported = []struct {
	words []string
	why   string
}{
	{[]string{"wan2.2-t2v-a14b", "wan2.2-i2v-a14b", "wan2.2_t2v_a14b", "a14b"}, "Wan 2.2 A14B runs as two models, of high and low noise, which the comfyui plugin doesn't deploy yet; Wan2.2-TI2V-5B and Wan2.1-T2V are supported"},
	{[]string{"qwen-image-edit", "qwen_image_edit", "qwen-image-2.1-edit", "qwen-image-2.1-turbo-edit", "flux.1-kontext", "flux1-kontext", "kontext"}, "it edits images, and the comfyui plugin's workflows make them from text only"},
	{[]string{"i2v", "flf2v"}, "it makes video from images, and the comfyui plugin's workflows make it from text only"},
}

// Names are what a family is told from: the repository's name and its
// base models, in lower case.
func Names(repo string, info *hf.Info) []string {
	names := []string{strings.ToLower(repo)}
	if i := strings.IndexByte(repo, '/'); i >= 0 {
		names = append(names, strings.ToLower(repo[i+1:]))
	}
	for _, b := range info.BaseModels() {
		if b = strings.ToLower(b); !slices.Contains(names, b) {
			names = append(names, b)
		}
	}
	return names
}

// Detect tells which family a repository's model is, from what Hugging
// Face says of it. info may be nil, as for a mirror that doesn't answer:
// the repository's name alone decides then.
func Detect(repo string, info *hf.Info) (*Family, error) {
	names := Names(repo, info)
	arch := ""
	if info != nil {
		arch = strings.ToLower(info.Architecture)
	}
	var found []*Family
	for _, f := range families {
		if arch != "" && !slices.Contains(f.Archs, arch) {
			continue
		}
		if f.Match(names) {
			found = append(found, f)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	for _, u := range unsupported {
		if has(names, u.words...) {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnknownFamily, repo, u.why)
		}
	}
	what := ""
	if arch != "" {
		what = fmt.Sprintf(" (its GGUF architecture is %q)", arch)
	}
	if len(found) > 1 {
		ids := make([]string, len(found))
		for i, f := range found {
			ids[i] = f.ID
		}
		return nil, fmt.Errorf("%w: %s%s could be %s: choose its family", ErrUnknownFamily, repo, what, strings.Join(ids, " or "))
	}
	return nil, fmt.Errorf("%w: can't tell which model %s is%s; choose its family if it's one of %s, or run it with the ComfyUI community task",
		ErrUnknownFamily, repo, what, strings.Join(IDs(), ", "))
}

// Check tells whether a model the user said is of family f may be: its
// architecture, when Hugging Face says it, must be the family's.
func Check(f *Family, repo string, info *hf.Info) error {
	if info == nil || info.Architecture == "" {
		return nil
	}
	arch := strings.ToLower(info.Architecture)
	if !slices.Contains(f.Archs, arch) {
		return fmt.Errorf("comfy: %s is a %q model, not %s, whose GGUF is %q", repo, arch, f.Name["en"], strings.Join(f.Archs, `" or "`))
	}
	return nil
}

// IDs are the families' IDs.
func IDs() []string {
	ids := make([]string, len(families))
	for i, f := range families {
		ids[i] = f.ID
	}
	return ids
}
