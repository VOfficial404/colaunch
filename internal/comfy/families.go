// Package comfy is Colaunch's side of ComfyUI: the families of diffusion
// models the comfyui plugin deploys as GGUF, with the files each needs
// beside its own and the workflows that run it, and a client for
// ComfyUI's API that runs those workflows.
package comfy

import (
	"fmt"
	"strings"
)

// Kind is what a family makes.
type Kind string

const (
	Image Kind = "image"
	Video Kind = "video"
)

// Params are what a generation asks for. Zero values take the family's
// defaults; Seed < 0 picks one at random.
type Params struct {
	Prompt, Negative string
	Width, Height    int
	Steps            int
	CFG              float64
	Seed             int64
	// Denoise applies to image-to-image. Zero uses DefaultDenoise.
	Denoise float64
	// Frames and FPS are a video's; Count is how many to make at once.
	Frames, FPS, Count int
}

// File is a file a family needs beside its diffusion model: a text
// encoder or a VAE, from a Hugging Face repository that anyone may
// download without a token, at a commit, so that it can't change under
// its checksum.
type File struct {
	Repo     string
	Revision string
	Path     string
	SHA256   string
	Size     int64
	// Dir is the folder of ComfyUI's models/ it goes in, as "vae".
	Dir string
}

// Name is the file's name in Dir, as ComfyUI's loaders list it.
func (f File) Name() string { return f.Path[strings.LastIndexByte(f.Path, '/')+1:] }

// Family is a kind of diffusion model, its defaults, and how to run it.
type Family struct {
	ID string
	// Name is in "en", "zh-Hans" and "zh-Hant".
	Name     map[string]string
	Kind     Kind
	Defaults Params
	// Sizes are the recommended widths and heights.
	Sizes [][2]int
	// Negative tells whether the family uses a negative prompt; those
	// sampling without classifier-free guidance ignore it.
	Negative bool

	// Archs are the GGUF general.architecture values of its models.
	Archs []string
	// Match tells, from the repository's name and its base models, all
	// in lower case, whether a model is of the family.
	Match func(names []string) bool
	// Files are the text encoders and VAE it needs.
	Files []File
	// Overhead is the GPU memory a generation takes beside the weights,
	// at the default size.
	Overhead int64
	// Step is what widths and heights are a multiple of.
	Step int
	// MaxFrames is the most frames a video may have, 0 for images.
	MaxFrames int

	build func(m Models, p Params, inputImage string) *Graph
}

// SupportsInputImage reports whether the family can start from a still image.
func (f *Family) SupportsInputImage() bool {
	if f == nil || f.build == nil {
		return false
	}
	switch f.ID {
	case "qwen-image", "qwen-image-2.1", "qwen-image-2.1-turbo", "flux1-dev", "flux1-schnell", "z-image", "z-image-turbo", "wan22-ti2v-5b":
		return true
	default:
		return false
	}
}

// Models are the names of the files a workflow loads, as ComfyUI's
// loaders list them.
type Models struct {
	// UNet is the diffusion model's GGUF, under models/diffusion_models.
	UNet string
	// LoRA is "" for none, else a file under models/loras.
	LoRA         string
	LoRAStrength float64
}

const gib = 1 << 30

// The companion files, checked against Hugging Face's API on 2026-11-25:
// each repository's tree at the commit below lists the file with its size
// and LFS sha256, the repository is neither gated nor private, and
// resolving the file without a token redirects to it (HTTP 302/307).
var (
	// https://huggingface.co/Comfy-Org/Qwen-Image_ComfyUI, commit 1f12b17
	qwenTextEncoder = File{Repo: "Comfy-Org/Qwen-Image_ComfyUI", Revision: "1f12b17be14c89b026c51a91d67c32f84bb047bc",
		Path: "split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors", Dir: "text_encoders", Size: 9384670680,
		SHA256: "cb5636d852a0ea6a9075ab1bef496c0db7aef13c02350571e388aea959c5c0b4"}
	qwenVAE = File{Repo: "Comfy-Org/Qwen-Image_ComfyUI", Revision: "1f12b17be14c89b026c51a91d67c32f84bb047bc",
		Path: "split_files/vae/qwen_image_vae.safetensors", Dir: "vae", Size: 253806246,
		SHA256: "a70580f0213e67967ee9c95f05bb400e8fb08307e017a924bf3441223e023d1f"}

	// https://huggingface.co/Comfy-Org/Qwen-Image-2.1, commit aacf31a.
	// W4A8 is smaller than the template's INT8 ConvRot encoder.
	qwen21TextEncoder = File{Repo: "Comfy-Org/Qwen-Image-2.1", Revision: "aacf31a6d48e707f352ecba714cee0680da3479b",
		Path: "text_encoders/qwen3vl_8b_w4a8.safetensors", Dir: "text_encoders", Size: 6312105364,
		SHA256: "7754425e55e7bea2bfde4dde59a4cc236cb44e5ee9c215ea66ef8d47012824eb"}
	qwen21VAE = File{Repo: "Comfy-Org/Qwen-Image-2.1", Revision: "aacf31a6d48e707f352ecba714cee0680da3479b",
		Path: "vae/qwen_image_2.1_vae_bf16.safetensors", Dir: "vae", Size: 675509688,
		SHA256: "bb21f7473051e1ac368515dd3f2e15cd44d7a11748ee8823e1ddca3e4876b7c9"}

	// https://huggingface.co/comfyanonymous/flux_text_encoders, commit 6af2a98
	clipL = File{Repo: "comfyanonymous/flux_text_encoders", Revision: "6af2a98e3f615bdfa612fbd85da93d1ed5f69ef5",
		Path: "clip_l.safetensors", Dir: "text_encoders", Size: 246144152,
		SHA256: "660c6f5b1abae9dc498ac2d21e1347d2abdb0cf6c0c0c8576cd796491d9a6cdd"}
	t5xxlFP8 = File{Repo: "comfyanonymous/flux_text_encoders", Revision: "6af2a98e3f615bdfa612fbd85da93d1ed5f69ef5",
		Path: "t5xxl_fp8_e4m3fn.safetensors", Dir: "text_encoders", Size: 4893934904,
		SHA256: "7d330da4816157540d6bb7838bf63a0f02f573fc48ca4d8de34bb0cbfd514f09"}
	// FLUX.1's VAE, ae.safetensors. black-forest-labs' own repositories
	// are gated; Comfy-Org's copy of it, with Lumina 2, isn't.
	// https://huggingface.co/Comfy-Org/Lumina_Image_2.0_Repackaged, commit 5b07254
	fluxVAE = File{Repo: "Comfy-Org/Lumina_Image_2.0_Repackaged", Revision: "5b072540ef86570fecb8249c505f23d5bdeb88cd",
		Path: "split_files/vae/ae.safetensors", Dir: "vae", Size: 335304388,
		SHA256: "afc8e28272cd15db3919bacdb6918ce9c1ed22e96cb12c4d5ed0fba823529e38"}

	// https://huggingface.co/Comfy-Org/z_image_turbo, commit 6fc90a3. Its
	// ae.safetensors is FLUX.1's, the same sha256 as fluxVAE.
	qwen3TextEncoder = File{Repo: "Comfy-Org/z_image_turbo", Revision: "6fc90a3b1b653e935a0d175e260736de25b84df5",
		Path: "split_files/text_encoders/qwen_3_4b.safetensors", Dir: "text_encoders", Size: 8044982048,
		SHA256: "6c671498573ac2f7a5501502ccce8d2b08ea6ca2f661c458e708f36b36edfc5a"}

	// https://huggingface.co/Comfy-Org/Wan_2.1_ComfyUI_repackaged, commit d625832
	umt5FP8 = File{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Revision: "d6258323e18f14c60d37899da97ca555f3118259",
		Path: "split_files/text_encoders/umt5_xxl_fp8_e4m3fn_scaled.safetensors", Dir: "text_encoders", Size: 6735906897,
		SHA256: "c3355d30191f1f066b26d93fba017ae9809dce6c627dda5f6a66eaa651204f68"}
	wan21VAE = File{Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Revision: "d6258323e18f14c60d37899da97ca555f3118259",
		Path: "split_files/vae/wan_2.1_vae.safetensors", Dir: "vae", Size: 253815318,
		SHA256: "2fc39d31359a4b0a64f55876d8ff7fa8d780956ae2cb13463b0223e15148976b"}
	// https://huggingface.co/Comfy-Org/Wan_2.2_ComfyUI_Repackaged, commit 5df3a4a
	wan22VAE = File{Repo: "Comfy-Org/Wan_2.2_ComfyUI_Repackaged", Revision: "5df3a4a3ed0628002308022198249dbbbd045d09",
		Path: "split_files/vae/wan2.2_vae.safetensors", Dir: "vae", Size: 1409400960,
		SHA256: "e40321bd36b9709991dae2530eb4ac303dd168276980d3e9bc4b6e2b75fed156"}
)

// Wan's negative prompt, as in ComfyUI's own Wan templates
// (comfyui-workflow-templates 0.11.111, text_to_video_wan.json).
const wanNegative = "色调艳丽，过曝，静态，细节模糊不清，字幕，风格，作品，画作，画面，静止，整体发灰，最差质量，低质量，JPEG压缩残留，丑陋的，残缺的，多余的手指，画得不好的手部，画得不好的脸部，畸形的，毁容的，形态畸形的肢体，手指融合，静止不动的画面，杂乱的背景，三条腿，背景人很多，倒着走"

// Qwen-Image's, as in image_qwen_Image_2512.json.
const qwenNegative = "低分辨率，低画质，肢体畸形，手指畸形，画面过饱和，蜡像感，人脸无细节，过度光滑，画面具有AI感。构图混乱。文字模糊，扭曲"

var fluxSizes = [][2]int{{1024, 1024}, {1152, 896}, {896, 1152}, {1216, 832}, {832, 1216}, {1344, 768}, {768, 1344}}

// The defaults follow ComfyUI's templates for each model
// (comfyui-workflow-templates 0.11.111): image_qwen_image.json,
// flux_dev_full_text_to_image.json, flux_schnell_full_text_to_image.json,
// image_z_image_turbo.json, image_z_image.json, text_to_video_wan.json
// and video_wan2_2_5B_ti2v.json.
var families = []*Family{
	{
		ID:       "qwen-image",
		Name:     map[string]string{"en": "Qwen-Image", "zh-Hans": "通义千问 Qwen-Image", "zh-Hant": "通義千問 Qwen-Image"},
		Kind:     Image,
		Defaults: Params{Width: 1328, Height: 1328, Steps: 20, CFG: 2.5, Seed: -1, Count: 1, Negative: qwenNegative},
		Sizes:    [][2]int{{1328, 1328}, {1664, 928}, {928, 1664}, {1472, 1104}, {1104, 1472}, {1584, 1056}, {1056, 1584}},
		Negative: true,
		Archs:    []string{"qwen_image"},
		Match: func(names []string) bool {
			return has(names, "qwen-image", "qwen_image") && !qwen21(names) && !qwenOther(names)
		},
		Files:    []File{qwenTextEncoder, qwenVAE},
		Overhead: 2 * gib,
		Step:     16,
		build:    buildQwenImage,
	},
	qwen21Family(false),
	qwen21Family(true),
	{
		ID:       "flux1-dev",
		Name:     map[string]string{"en": "FLUX.1 [dev]", "zh-Hans": "FLUX.1 [dev]", "zh-Hant": "FLUX.1 [dev]"},
		Kind:     Image,
		Defaults: Params{Width: 1024, Height: 1024, Steps: 20, CFG: 3.5, Seed: -1, Count: 1},
		Sizes:    fluxSizes,
		Archs:    []string{"flux"},
		Match: func(names []string) bool {
			return has(names, "flux.1-dev", "flux1-dev", "flux.1-krea-dev", "flux1-krea-dev", "flux-dev", "flux.1 dev") && !fluxOther(names)
		},
		Files:    []File{clipL, t5xxlFP8, fluxVAE},
		Overhead: 2 * gib,
		Step:     16,
		build:    buildFluxDev,
	},
	{
		ID:       "flux1-schnell",
		Name:     map[string]string{"en": "FLUX.1 [schnell]", "zh-Hans": "FLUX.1 [schnell]", "zh-Hant": "FLUX.1 [schnell]"},
		Kind:     Image,
		Defaults: Params{Width: 1024, Height: 1024, Steps: 4, CFG: 1, Seed: -1, Count: 1},
		Sizes:    fluxSizes,
		Archs:    []string{"flux"},
		Match: func(names []string) bool {
			return has(names, "schnell") && !fluxOther(names)
		},
		Files:    []File{clipL, t5xxlFP8, fluxVAE},
		Overhead: 2 * gib,
		Step:     16,
		build:    buildFluxSchnell,
	},
	{
		ID:       "z-image-turbo",
		Name:     map[string]string{"en": "Z-Image-Turbo", "zh-Hans": "造相 Z-Image-Turbo", "zh-Hant": "造相 Z-Image-Turbo"},
		Kind:     Image,
		Defaults: Params{Width: 1024, Height: 1024, Steps: 8, CFG: 1, Seed: -1, Count: 1},
		Sizes:    [][2]int{{1024, 1024}, {1152, 896}, {896, 1152}, {1280, 720}, {720, 1280}, {1536, 1536}},
		Archs:    []string{"lumina2"},
		Match: func(names []string) bool {
			return has(names, "z-image-turbo", "z_image_turbo", "zimage-turbo")
		},
		Files:    []File{qwen3TextEncoder, fluxVAE},
		Overhead: 2 * gib,
		Step:     16,
		build:    buildZImage,
	},
	{
		ID:       "z-image",
		Name:     map[string]string{"en": "Z-Image", "zh-Hans": "造相 Z-Image", "zh-Hant": "造相 Z-Image"},
		Kind:     Image,
		Defaults: Params{Width: 1024, Height: 1024, Steps: 25, CFG: 4, Seed: -1, Count: 1},
		Sizes:    [][2]int{{1024, 1024}, {1152, 896}, {896, 1152}, {1280, 720}, {720, 1280}, {1536, 1536}},
		Negative: true,
		Archs:    []string{"lumina2"},
		Match: func(names []string) bool {
			return has(names, "z-image", "z_image") && !has(names, "turbo", "edit")
		},
		Files:    []File{qwen3TextEncoder, fluxVAE},
		Overhead: 2 * gib,
		Step:     16,
		build:    buildZImage,
	},
	{
		ID:       "wan21-t2v",
		Name:     map[string]string{"en": "Wan 2.1 text to video", "zh-Hans": "万相 Wan 2.1 文生视频", "zh-Hant": "萬相 Wan 2.1 文生影片"},
		Kind:     Video,
		Defaults: Params{Width: 832, Height: 480, Steps: 30, CFG: 6, Seed: -1, Frames: 33, FPS: 16, Count: 1, Negative: wanNegative},
		Sizes:    [][2]int{{832, 480}, {480, 832}, {624, 624}, {1280, 720}, {720, 1280}},
		Negative: true,
		Archs:    []string{"wan"},
		Match: func(names []string) bool {
			return has(names, "wan2.1-t2v", "wan2.1_t2v", "wan2_1-t2v", "wan2.1-t2v-") && !wanOther(names)
		},
		Files:     []File{umt5FP8, wan21VAE},
		Overhead:  3 * gib,
		Step:      16,
		MaxFrames: 121,
		build:     buildWan21,
	},
	{
		ID:       "wan22-ti2v-5b",
		Name:     map[string]string{"en": "Wan 2.2 5B text to video", "zh-Hans": "万相 Wan 2.2 5B 文生视频", "zh-Hant": "萬相 Wan 2.2 5B 文生影片"},
		Kind:     Video,
		Defaults: Params{Width: 1280, Height: 704, Steps: 20, CFG: 5, Seed: -1, Frames: 49, FPS: 24, Count: 1, Negative: wanNegative},
		Sizes:    [][2]int{{1280, 704}, {704, 1280}, {960, 544}, {544, 960}},
		Negative: true,
		Archs:    []string{"wan"},
		Match: func(names []string) bool {
			return has(names, "wan2.2-ti2v-5b", "wan2.2_ti2v_5b", "wan2_2-ti2v-5b") && !has(names, "fun", "vace", "animate")
		},
		Files:     []File{umt5FP8, wan22VAE},
		Overhead:  4 * gib,
		Step:      32,
		MaxFrames: 121,
		build:     buildWan22,
	},
}

func qwen21Family(turbo bool) *Family {
	id, name, steps := "qwen-image-2.1", "Qwen-Image 2.1", 25
	if turbo {
		id, name, steps = "qwen-image-2.1-turbo", "Qwen-Image 2.1 Turbo", qwen21TurboSteps
	}
	return &Family{
		ID:       id,
		Name:     map[string]string{"en": name, "zh-Hans": "通义千问 " + name, "zh-Hant": "通義千問 " + name},
		Kind:     Image,
		Defaults: Params{Width: 1024, Height: 1024, Steps: steps, CFG: 1, Seed: -1, Count: 1},
		Sizes:    fluxSizes,
		Negative: true,
		Archs:    []string{"qwen_image", "qwen_image21"},
		Match: func(names []string) bool {
			return qwen21(names) && !qwenOther(names) && has(names, "turbo") == turbo
		},
		Files:    []File{qwen21TextEncoder, qwen21VAE},
		Overhead: 2 * gib,
		Step:     16,
		build: func(m Models, p Params, inputImage string) *Graph {
			return buildQwen21(m, p, inputImage, id, turbo)
		},
	}
}

func qwen21(names []string) bool {
	return has(names, "qwen-image-2.1", "qwen_image_2.1", "qwen-image-2_1", "qwen_image_2_1")
}

func qwenOther(names []string) bool {
	return has(names, "edit", "layered", "controlnet", "background-removal", "background_removal")
}

// fluxOther are FLUX.1's models that take an image, or that aren't FLUX.1
// itself though their GGUF says "flux", as Chroma.
func fluxOther(names []string) bool {
	return has(names, "kontext", "fill", "redux", "canny", "depth", "chroma", "flux.2", "flux2", "flux.3", "flux3")
}

// wanOther are Wan's models for other jobs than text to video.
func wanOther(names []string) bool {
	return has(names, "i2v", "vace", "flf2v", "fun", "camera", "animate", "s2v", "infinitetalk", "magref")
}

// has tells whether one of names has one of words in it.
func has(names []string, words ...string) bool {
	for _, n := range names {
		for _, w := range words {
			if strings.Contains(n, w) {
				return true
			}
		}
	}
	return false
}

// Families are the families of models the comfyui plugin runs.
func Families() []*Family {
	return append([]*Family(nil), families...)
}

// FamilyByID returns a family by its ID, nil for none.
func FamilyByID(id string) *Family {
	for _, f := range families {
		if f.ID == id {
			return f
		}
	}
	return nil
}

// Need estimates the GPU memory a quantization of the family's model of
// size bytes needs to keep everything on the GPU: the model, the text
// encoders, the VAE, and what a generation takes beside them.
func (f *Family) Need(size int64) int64 {
	n := size + size/20 + f.Overhead
	for _, c := range f.Files {
		n += c.Size
	}
	return n
}

// NeedLeast is the least GPU memory the model runs in without its
// weights on the CPU: ComfyUI moves the text encoders off the GPU once
// they've read the prompt.
func (f *Family) NeedLeast(size int64) int64 {
	n := size + size/20 + f.Overhead
	for _, c := range f.Files {
		if c.Dir == "vae" {
			n += c.Size
		}
	}
	return n
}

// Fill returns p with the family's defaults for what it leaves out, and
// its values brought within what the family's workflow takes.
func (f *Family) Fill(p Params) Params {
	d := f.Defaults
	if strings.TrimSpace(p.Negative) == "" {
		p.Negative = d.Negative
	}
	if !f.Negative {
		p.Negative = ""
	}
	if p.Width <= 0 {
		p.Width = d.Width
	}
	if p.Height <= 0 {
		p.Height = d.Height
	}
	step := max(f.Step, 8)
	p.Width = clamp(roundTo(p.Width, step), step*8, 4096)
	p.Height = clamp(roundTo(p.Height, step), step*8, 4096)
	if p.Steps <= 0 {
		p.Steps = d.Steps
	}
	p.Steps = min(p.Steps, 200)
	if p.CFG <= 0 {
		p.CFG = d.CFG
	}
	p.CFG = min(p.CFG, 30)
	if p.Count <= 0 {
		p.Count = max(d.Count, 1)
	}
	p.Count = min(p.Count, 8)
	if f.Kind == Video {
		if p.Frames <= 0 {
			p.Frames = d.Frames
		}
		// Wan's VAE takes 4n+1 frames.
		p.Frames = clamp((p.Frames-1+2)/4*4+1, 5, max(f.MaxFrames, 5))
		if p.FPS <= 0 {
			p.FPS = d.FPS
		}
		p.FPS = clamp(p.FPS, 1, 60)
		p.Count = min(p.Count, 2)
	} else {
		p.Frames, p.FPS = 0, 0
	}
	return p
}

func roundTo(n, step int) int { return (n + step/2) / step * step }

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }

// String is the family's English name.
func (f *Family) String() string { return fmt.Sprintf("%s (%s)", f.Name["en"], f.ID) }
