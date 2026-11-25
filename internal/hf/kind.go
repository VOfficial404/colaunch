package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	u := fmt.Sprintf("%s/api/models/%s?expand%%5B%%5D=gguf&expand%%5B%%5D=pipeline_tag&expand%%5B%%5D=tags", endpoint, repo)
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
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&r); err != nil {
		return nil, err
	}
	info := &Info{Pipeline: r.Pipeline, Tags: r.Tags}
	if r.GGUF != nil {
		info.Architecture = r.GGUF.Architecture
	}
	return info, nil
}

// Pipelines llama-server has nothing to do with.
var otherPipelines = map[string]bool{
	"text-to-image": true, "image-to-image": true, "unconditional-image-generation": true,
	"text-to-video": true, "image-to-video": true, "video-to-video": true,
	"text-to-3d": true, "image-to-3d": true,
}

// GGUF architectures of diffusion models, which stable-diffusion.cpp and
// ComfyUI-GGUF read and llama.cpp doesn't.
var diffusionArchitectures = []string{
	"qwen_image", "flux", "sd", "sd1", "sd2", "sdxl", "sd3", "wan", "hunyuan", "hidream",
	"ltxv", "cosmos", "lumina", "chroma", "aura", "pixart", "mochi", "cogvideo", "kontext",
}

// CheckServable returns ErrNotLLM, with what the model is, for a model
// llama.cpp's server can't run.
func (i *Info) CheckServable() error {
	if i == nil {
		return nil
	}
	arch := strings.ToLower(i.Architecture)
	diffusion := false
	for _, a := range diffusionArchitectures {
		if arch == a || strings.HasPrefix(arch, a+"_") {
			diffusion = true
		}
	}
	if !diffusion && !otherPipelines[i.Pipeline] {
		return nil
	}
	what := i.Pipeline
	if what == "" {
		what = "diffusion"
	}
	if arch != "" {
		what += ", " + arch
	}
	return fmt.Errorf("%w: it is a %s model; run it with the ComfyUI task instead", ErrNotLLM, what)
}
