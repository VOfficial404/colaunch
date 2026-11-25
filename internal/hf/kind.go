package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Info is what Hugging Face says a repository's model is.
type Info struct {
	// Architecture is the GGUF files' general.architecture, as "qwen3"
	// or "qwen_image".
	Architecture string
	// Pipeline is the task the model is for, as "text-generation" or
	// "text-to-image".
	Pipeline string
	Tags     []string
	// SHA is the commit of the main branch, "" when not told.
	SHA string
}

// BaseModels are the models the repository's says it's made from, by its
// "base_model:" tags, as "Qwen/Qwen-Image" for a quantization of it.
func (i *Info) BaseModels() []string {
	if i == nil {
		return nil
	}
	var out []string
	for _, t := range i.Tags {
		rest, ok := strings.CutPrefix(t, "base_model:")
		if !ok {
			continue
		}
		// "base_model:quantized:Qwen/Qwen-Image" says how it's made.
		if kind, repo, ok := strings.Cut(rest, ":"); ok && !strings.Contains(kind, "/") {
			rest = repo
		}
		if rest != "" && !slices.Contains(out, rest) {
			out = append(out, rest)
		}
	}
	return out
}

// ErrNotLLM is returned for a model llama.cpp's server can't run, as an
// image or video model made for ComfyUI.
var ErrNotLLM = errors.New("hf: llama.cpp can't serve this model")

// FetchInfo asks Hugging Face what a repository's model is. Mirrors may not
// answer it: callers go on without it then.
func FetchInfo(ctx context.Context, repo, token string) (*Info, error) {
	endpoint, err := CheckEndpoint(Endpoint)
	if err != nil {
		return nil, err
	}
	token = TokenFor(endpoint, token)
	u := fmt.Sprintf("%s/api/models/%s?expand%%5B%%5D=gguf&expand%%5B%%5D=pipeline_tag&expand%%5B%%5D=tags&expand%%5B%%5D=sha", endpoint, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("hf: %s", resp.Status)
	}
	var r struct {
		GGUF *struct {
			Architecture string `json:"architecture"`
		} `json:"gguf"`
		Pipeline string   `json:"pipeline_tag"`
		Tags     []string `json:"tags"`
		SHA      string   `json:"sha"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&r); err != nil {
		return nil, err
	}
	info := &Info{Pipeline: r.Pipeline, Tags: r.Tags}
	if commitPattern.MatchString(r.SHA) {
		info.SHA = r.SHA
	}
	if r.GGUF != nil {
		info.Architecture = r.GGUF.Architecture
	}
	return info, nil
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Pipelines llama-server has nothing to do with.
var otherPipelines = map[string]bool{
	"text-to-image": true, "image-to-image": true, "unconditional-image-generation": true,
	"text-to-video": true, "image-to-video": true, "video-to-video": true,
	"text-to-3d": true, "image-to-3d": true,
}

// GGUF architectures of diffusion models, which stable-diffusion.cpp and
// ComfyUI-GGUF read and llama.cpp doesn't.
var diffusionArchitectures = []string{
	"qwen_image", "qwen_image21", "flux", "sd", "sd1", "sd2", "sdxl", "sd3", "wan", "hunyuan", "hyvid", "hidream",
	"ltxv", "cosmos", "lumina", "lumina2", "chroma", "aura", "pixart", "mochi", "cogvideo", "kontext",
}

// IsDiffusion reports whether a GGUF architecture is a diffusion model's.
func IsDiffusion(arch string) bool {
	arch = strings.ToLower(arch)
	for _, a := range diffusionArchitectures {
		if arch == a || strings.HasPrefix(arch, a+"_") {
			return true
		}
	}
	return false
}

// CheckServable returns ErrNotLLM, with what the model is, for a model
// llama.cpp's server can't run.
func (i *Info) CheckServable() error {
	if i == nil {
		return nil
	}
	arch := strings.ToLower(i.Architecture)
	if !IsDiffusion(arch) && !otherPipelines[i.Pipeline] {
		return nil
	}
	what := i.Pipeline
	if what == "" {
		what = "diffusion"
	}
	if arch != "" {
		what += ", " + arch
	}
	return fmt.Errorf("%w: it is a %s model, for ComfyUI", ErrNotLLM, what)
}
