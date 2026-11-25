package comfy

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
)

// The nodes the workflows use, with their inputs in the order ComfyUI's
// web page keeps their widgets, and their outputs. Checked against the
// source of ComfyUI v0.46.0 (nodes.py, comfy_extras/nodes_sd3.py,
// nodes_flux.py, nodes_hunyuan.py, nodes_wan.py, nodes_video.py,
// nodes_model_advanced.py, nodes_qwen.py, nodes_custom_sampler.py) and of
// ComfyUI-GGUF at edd981b (nodes.py),
// and by posting every family's workflow to a ComfyUI of those versions
// (scripts/validate_comfy_workflows.py).
var nodeTypes = map[string]struct {
	// widgets are the inputs that are values, in order; "$control" is
	// the web page's "control after generate" beside a seed.
	widgets []string
	outputs []string
}{
	// ComfyUI-GGUF: UnetLoaderGGUF lists models/diffusion_models (and
	// models/unet) files ending in .gguf.
	"UnetLoaderGGUF":          {[]string{"unet_name"}, []string{"MODEL"}},
	"CLIPLoader":              {[]string{"clip_name", "type", "device"}, []string{"CLIP"}},
	"DualCLIPLoader":          {[]string{"clip_name1", "clip_name2", "type", "device"}, []string{"CLIP"}},
	"VAELoader":               {[]string{"vae_name"}, []string{"VAE"}},
	"LoraLoaderModelOnly":     {[]string{"lora_name", "strength_model"}, []string{"MODEL"}},
	"ModelSamplingAuraFlow":   {[]string{"shift"}, []string{"MODEL"}},
	"ModelSamplingSD3":        {[]string{"shift"}, []string{"MODEL"}},
	"CLIPTextEncode":          {[]string{"text"}, []string{"CONDITIONING"}},
	"TextEncodeQwenImage21":   {[]string{"prompt", "negative_prompt", "resolution"}, []string{"CONDITIONING", "CONDITIONING", "LATENT"}},
	"QwenImage21Cache":        {[]string{"device", "dtype"}, []string{"MODEL"}},
	"EmptyLatentImage":        {[]string{"width", "height", "batch_size"}, []string{"LATENT"}},
	"ManualSigmas":            {[]string{"sigmas"}, []string{"SIGMAS"}},
	"RandomNoise":             {[]string{"noise_seed", "$control"}, []string{"NOISE"}},
	"KSamplerSelect":          {[]string{"sampler_name"}, []string{"SAMPLER"}},
	"CFGGuider":               {[]string{"cfg"}, []string{"GUIDER"}},
	"SamplerCustomAdvanced":   {nil, []string{"LATENT", "LATENT"}},
	"FluxGuidance":            {[]string{"guidance"}, []string{"CONDITIONING"}},
	"ConditioningZeroOut":     {nil, []string{"CONDITIONING"}},
	"EmptySD3LatentImage":     {[]string{"width", "height", "batch_size"}, []string{"LATENT"}},
	"EmptyHunyuanLatentVideo": {[]string{"width", "height", "length", "batch_size"}, []string{"LATENT"}},
	"Wan22ImageToVideoLatent": {[]string{"width", "height", "length", "batch_size"}, []string{"LATENT"}},
	"KSampler":                {[]string{"seed", "$control", "steps", "cfg", "sampler_name", "scheduler", "denoise"}, []string{"LATENT"}},
	"VAEDecode":               {nil, []string{"IMAGE"}},
	"SaveImage":               {[]string{"filename_prefix"}, nil},
	"CreateVideo":             {[]string{"fps"}, []string{"VIDEO"}},
	// SaveVideo's format is a dynamic combo: its codec, an input of the
	// format chosen, is "format.codec" in the API's workflow.
	"SaveVideo": {[]string{"filename_prefix", "format", "format.codec"}, nil},
}

// These nodes name their outputs individually, including sockets of the same type.
var nodeOutputNames = map[string][]string{
	"TextEncodeQwenImage21": {"positive", "negative", "latent"},
	"SamplerCustomAdvanced": {"output", "denoised_output"},
}

// Link is an input that is another node's output.
type Link struct {
	Node string
	Slot int
}

// Node is a node of a workflow.
type Node struct {
	ID     string
	Class  string
	Title  string
	Inputs map[string]any
}

// Graph is a workflow, its nodes in the order they were added.
type Graph struct {
	Nodes []*Node
	// Models are where the web page's workflow says its files come from.
	Models []File
}

func (g *Graph) add(class, title string, inputs map[string]any) Link {
	id := strconv.Itoa(len(g.Nodes) + 1)
	g.Nodes = append(g.Nodes, &Node{ID: id, Class: class, Title: title, Inputs: inputs})
	return Link{Node: id}
}

// Node returns the node of an ID, nil for none.
func (g *Graph) Node(id string) *Node {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// API is the workflow as ComfyUI's /prompt takes it.
func (g *Graph) API() map[string]any {
	out := make(map[string]any, len(g.Nodes))
	for _, n := range g.Nodes {
		inputs := make(map[string]any, len(n.Inputs))
		for k, v := range n.Inputs {
			if l, ok := v.(Link); ok {
				inputs[k] = []any{l.Node, l.Slot}
			} else {
				inputs[k] = v
			}
		}
		node := map[string]any{"class_type": n.Class, "inputs": inputs}
		if n.Title != "" {
			node["_meta"] = map[string]any{"title": n.Title}
		}
		out[n.ID] = node
	}
	return out
}

// Workflow builds the family's workflow for p, as ComfyUI's API takes it.
func (f *Family) Workflow(m Models, p Params) *Graph {
	p = f.Fill(p)
	if p.Seed < 0 {
		p.Seed = rand.Int64N(1 << 50)
	}
	if m.LoRA != "" && m.LoRAStrength == 0 {
		m.LoRAStrength = 1
	}
	return f.build(m, p)
}

// common parts of the workflows.

func (g *Graph) unet(m Models) Link {
	model := g.add("UnetLoaderGGUF", "", map[string]any{"unet_name": m.UNet})
	if m.LoRA != "" {
		model = g.add("LoraLoaderModelOnly", "", map[string]any{"model": model, "lora_name": m.LoRA, "strength_model": m.LoRAStrength})
	}
	return model
}

func (g *Graph) clip(f File, typ string) Link {
	g.Models = append(g.Models, f)
	return g.add("CLIPLoader", "", map[string]any{"clip_name": f.Name(), "type": typ, "device": "default"})
}

func (g *Graph) vae(f File) Link {
	g.Models = append(g.Models, f)
	return g.add("VAELoader", "", map[string]any{"vae_name": f.Name()})
}

func (g *Graph) text(clip Link, text, title string) Link {
	return g.add("CLIPTextEncode", title, map[string]any{"clip": clip, "text": text})
}

func (g *Graph) sample(model, pos, neg, latent Link, p Params, sampler, scheduler string) Link {
	return g.add("KSampler", "", map[string]any{
		"model": model, "positive": pos, "negative": neg, "latent_image": latent,
		"seed": p.Seed, "steps": p.Steps, "cfg": p.CFG, "sampler_name": sampler, "scheduler": scheduler, "denoise": 1.0,
	})
}

func (g *Graph) saveImage(samples, vae Link, prefix string) {
	img := g.add("VAEDecode", "", map[string]any{"samples": samples, "vae": vae})
	g.add("SaveImage", "", map[string]any{"images": img, "filename_prefix": "colaunch/" + prefix})
}

func (g *Graph) saveVideo(samples, vae Link, p Params, prefix string) {
	img := g.add("VAEDecode", "", map[string]any{"samples": samples, "vae": vae})
	video := g.add("CreateVideo", "", map[string]any{"images": img, "fps": float64(p.FPS)})
	g.add("SaveVideo", "", map[string]any{"video": video, "filename_prefix": "colaunch/" + prefix, "format": "mp4", "format.codec": "h264"})
}

const (
	positiveTitle = "Positive prompt"
	negativeTitle = "Negative prompt"
)

// Qwen-Image: its Qwen2.5-VL text encoder, AuraFlow's sampling with a
// shift of 3.1, and SD3's empty latent.
func buildQwenImage(m Models, p Params) *Graph {
	g := &Graph{}
	model := g.add("ModelSamplingAuraFlow", "", map[string]any{"model": g.unet(m), "shift": 3.1})
	clip := g.clip(qwenTextEncoder, "qwen_image")
	vae := g.vae(qwenVAE)
	pos := g.text(clip, p.Prompt, positiveTitle)
	neg := g.text(clip, p.Negative, negativeTitle)
	latent := g.add("EmptySD3LatentImage", "", map[string]any{"width": p.Width, "height": p.Height, "batch_size": p.Count})
	g.saveImage(g.sample(model, pos, neg, latent, p, "euler", "simple"), vae, "qwen-image")
	return g
}

const (
	qwen21TurboSteps  = 8
	qwen21TurboSigmas = "1.0, 0.978453, 0.95418, 0.926626, 0.89508, 0.845148, 0.704534, 0.414568, 0"
)

// Both 2.1 text-to-image templates use the same encoder and cache. The
// sampler adjusts the empty latent to the model's 64 channels and 16x scale.
func buildQwen21(m Models, p Params, prefix string, turbo bool) *Graph {
	g := &Graph{}
	model := g.add("QwenImage21Cache", "", map[string]any{"model": g.unet(m), "device": "auto", "dtype": "default"})
	clip := g.clip(qwen21TextEncoder, "qwen_image")
	vae := g.vae(qwen21VAE)
	pos := g.add("TextEncodeQwenImage21", "", map[string]any{
		"clip": clip, "prompt": p.Prompt, "negative_prompt": p.Negative, "resolution": 1024,
	})
	neg := Link{Node: pos.Node, Slot: 1}
	latent := g.add("EmptyLatentImage", "", map[string]any{"width": p.Width, "height": p.Height, "batch_size": p.Count})
	var samples Link
	if turbo && p.Steps == qwen21TurboSteps {
		sigmas := g.add("ManualSigmas", "", map[string]any{"sigmas": qwen21TurboSigmas})
		noise := g.add("RandomNoise", "", map[string]any{"noise_seed": p.Seed})
		guider := g.add("CFGGuider", "", map[string]any{"model": model, "positive": pos, "negative": neg, "cfg": p.CFG})
		sampler := g.add("KSamplerSelect", "", map[string]any{"sampler_name": "euler"})
		samples = g.add("SamplerCustomAdvanced", "", map[string]any{
			"noise": noise, "guider": guider, "sampler": sampler, "sigmas": sigmas, "latent_image": latent,
		})
	} else {
		// An explicitly different step count uses ComfyUI's simple schedule.
		samples = g.sample(model, pos, neg, latent, p, "euler", "simple")
	}
	g.saveImage(samples, vae, prefix)
	return g
}

// FLUX.1 [dev] is guidance distilled: its guidance, FluxGuidance's, is
// what CFG says, and the sampler's own CFG stays at 1, with no negative
// prompt.
func buildFluxDev(m Models, p Params) *Graph {
	return buildFlux(m, p, true, "flux1-dev")
}

// FLUX.1 [schnell] takes 4 steps and no guidance at all.
func buildFluxSchnell(m Models, p Params) *Graph {
	return buildFlux(m, p, false, "flux1-schnell")
}

func buildFlux(m Models, p Params, guidance bool, prefix string) *Graph {
	g := &Graph{}
	model := g.unet(m)
	g.Models = append(g.Models, clipL, t5xxlFP8)
	clip := g.add("DualCLIPLoader", "", map[string]any{"clip_name1": clipL.Name(), "clip_name2": t5xxlFP8.Name(), "type": "flux", "device": "default"})
	vae := g.vae(fluxVAE)
	pos := g.text(clip, p.Prompt, positiveTitle)
	if guidance {
		pos = g.add("FluxGuidance", "", map[string]any{"conditioning": pos, "guidance": p.CFG})
	}
	neg := g.add("ConditioningZeroOut", "", map[string]any{"conditioning": pos})
	latent := g.add("EmptySD3LatentImage", "", map[string]any{"width": p.Width, "height": p.Height, "batch_size": p.Count})
	p.CFG = 1
	g.saveImage(g.sample(model, pos, neg, latent, p, "euler", "simple"), vae, prefix)
	return g
}

// Z-Image: Qwen3 4B as its text encoder, of the "lumina2" type, FLUX.1's
// VAE, AuraFlow's sampling with a shift of 3. Turbo samples without CFG,
// so without a negative prompt.
func buildZImage(m Models, p Params) *Graph {
	g := &Graph{}
	model := g.add("ModelSamplingAuraFlow", "", map[string]any{"model": g.unet(m), "shift": 3.0})
	clip := g.clip(qwen3TextEncoder, "lumina2")
	vae := g.vae(fluxVAE)
	pos := g.text(clip, p.Prompt, positiveTitle)
	var neg Link
	if p.Negative != "" || p.CFG > 1 {
		neg = g.text(clip, p.Negative, negativeTitle)
	} else {
		neg = g.add("ConditioningZeroOut", "", map[string]any{"conditioning": pos})
	}
	latent := g.add("EmptySD3LatentImage", "", map[string]any{"width": p.Width, "height": p.Height, "batch_size": p.Count})
	g.saveImage(g.sample(model, pos, neg, latent, p, "res_multistep", "simple"), vae, "z-image")
	return g
}

// Wan 2.1: UMT5-XXL, SD3's sampling with a shift of 8, Hunyuan Video's
// empty latent, and its 16 frames a second.
func buildWan21(m Models, p Params) *Graph {
	g := &Graph{}
	model := g.add("ModelSamplingSD3", "", map[string]any{"model": g.unet(m), "shift": 8.0})
	clip := g.clip(umt5FP8, "wan")
	vae := g.vae(wan21VAE)
	pos := g.text(clip, p.Prompt, positiveTitle)
	neg := g.text(clip, p.Negative, negativeTitle)
	latent := g.add("EmptyHunyuanLatentVideo", "", map[string]any{"width": p.Width, "height": p.Height, "length": p.Frames, "batch_size": p.Count})
	g.saveVideo(g.sample(model, pos, neg, latent, p, "uni_pc", "simple"), vae, p, "wan21")
	return g
}

// Wan 2.2 5B: its own VAE, which the latent is made with, at 24 frames a
// second.
func buildWan22(m Models, p Params) *Graph {
	g := &Graph{}
	model := g.add("ModelSamplingSD3", "", map[string]any{"model": g.unet(m), "shift": 8.0})
	clip := g.clip(umt5FP8, "wan")
	vae := g.vae(wan22VAE)
	pos := g.text(clip, p.Prompt, positiveTitle)
	neg := g.text(clip, p.Negative, negativeTitle)
	latent := g.add("Wan22ImageToVideoLatent", "", map[string]any{"vae": vae, "width": p.Width, "height": p.Height, "length": p.Frames, "batch_size": p.Count})
	g.saveVideo(g.sample(model, pos, neg, latent, p, "uni_pc", "simple"), vae, p, "wan22")
	return g
}

// UI is the workflow as ComfyUI's web page saves it, to open from its
// workflows: its nodes laid out in columns by how far they are from the
// loaders, and the files they load with where to download them.
func (g *Graph) UI(endpoint string) ([]byte, error) {
	depth := map[string]int{}
	var depthOf func(n *Node) int
	depthOf = func(n *Node) int {
		if d, ok := depth[n.ID]; ok {
			return d
		}
		d := 0
		for _, v := range n.Inputs {
			if l, ok := v.(Link); ok {
				if from := g.Node(l.Node); from != nil {
					d = max(d, depthOf(from)+1)
				}
			}
		}
		depth[n.ID] = d
		return d
	}
	rows := map[int]int{}
	type link struct {
		id, from, fromSlot, to, toSlot int
		typ                            string
	}
	var links []link
	nodes := make([]map[string]any, 0, len(g.Nodes))
	outLinks := map[Link][]int{}
	byID := map[string]map[string]any{}
	order := append([]*Node(nil), g.Nodes...)
	sort.SliceStable(order, func(i, j int) bool { return depthOf(order[i]) < depthOf(order[j]) })
	for i, n := range order {
		spec, ok := nodeTypes[n.Class]
		if !ok {
			return nil, fmt.Errorf("comfy: no layout for %s", n.Class)
		}
		id, _ := strconv.Atoi(n.ID)
		d := depthOf(n)
		row := rows[d]
		rows[d]++
		var inputs []map[string]any
		names := make([]string, 0, len(n.Inputs))
		for k := range n.Inputs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			l, ok := n.Inputs[k].(Link)
			if !ok {
				continue
			}
			from := g.Node(l.Node)
			typ := nodeTypes[from.Class].outputs[l.Slot]
			lid := len(links) + 1
			fromID, _ := strconv.Atoi(l.Node)
			links = append(links, link{lid, fromID, l.Slot, id, len(inputs), typ})
			outLinks[l] = append(outLinks[l], lid)
			inputs = append(inputs, map[string]any{"name": k, "type": typ, "link": lid})
		}
		widgets := []any{}
		for _, w := range spec.widgets {
			if w == "$control" {
				widgets = append(widgets, "randomize")
				continue
			}
			widgets = append(widgets, n.Inputs[w])
		}
		props := map[string]any{"Node name for S&R": n.Class}
		var models []map[string]any
		for _, w := range []string{"clip_name", "clip_name1", "clip_name2", "vae_name"} {
			name, _ := n.Inputs[w].(string)
			for _, f := range g.Models {
				if f.Name() == name {
					models = append(models, map[string]any{"name": name, "directory": f.Dir,
						"url": fmt.Sprintf("%s/%s/resolve/%s/%s", endpoint, f.Repo, f.Revision, f.Path)})
					break
				}
			}
		}
		if models != nil {
			props["models"] = models
		}
		node := map[string]any{
			"id": id, "type": n.Class, "pos": []int{d * 380, row * 260}, "size": []int{340, 200},
			"flags": map[string]any{}, "order": i, "mode": 0, "inputs": inputs, "properties": props,
			"widgets_values": widgets,
		}
		if inputs == nil {
			node["inputs"] = []any{}
		}
		if n.Title != "" {
			node["title"] = n.Title
		}
		nodes = append(nodes, node)
		byID[n.ID] = node
	}
	for _, n := range g.Nodes {
		var outputs []map[string]any
		for slot, typ := range nodeTypes[n.Class].outputs {
			ls := outLinks[Link{Node: n.ID, Slot: slot}]
			if ls == nil {
				ls = []int{}
			}
			name := typ
			if names := nodeOutputNames[n.Class]; names != nil {
				name = names[slot]
			}
			outputs = append(outputs, map[string]any{"name": name, "type": typ, "slot_index": slot, "links": ls})
		}
		if outputs == nil {
			byID[n.ID]["outputs"] = []any{}
		} else {
			byID[n.ID]["outputs"] = outputs
		}
	}
	jlinks := make([][]any, len(links))
	for i, l := range links {
		jlinks[i] = []any{l.id, l.from, l.fromSlot, l.to, l.toSlot, l.typ}
	}
	return json.MarshalIndent(map[string]any{
		"last_node_id": len(g.Nodes), "last_link_id": len(links),
		"nodes": nodes, "links": jlinks, "groups": []any{}, "config": map[string]any{},
		"extra": map[string]any{"colaunch": true}, "version": 0.4,
	}, "", " ")
}
