package comfy

import (
	"reflect"
	"testing"
)

func TestInputImageWorkflows(t *testing.T) {
	const filename = "colaunch-example.png"
	type imageSampler struct {
		name string
		vae  string
	}
	imageFamilies := map[string]imageSampler{
		"qwen-image":           {"euler", qwenVAE.Name()},
		"qwen-image-2.1":       {"euler", qwen21VAE.Name()},
		"qwen-image-2.1-turbo": {"euler", qwen21VAE.Name()},
		"flux1-dev":            {"euler", fluxVAE.Name()},
		"flux1-schnell":        {"euler", fluxVAE.Name()},
		"z-image":              {"res_multistep", fluxVAE.Name()},
		"z-image-turbo":        {"res_multistep", fluxVAE.Name()},
	}
	if (*Family)(nil).SupportsInputImage() || (&Family{ID: "unknown", Kind: Image}).SupportsInputImage() {
		t.Fatal("nil and unknown families should not accept input images")
	}
	for _, f := range Families() {
		t.Run(f.ID, func(t *testing.T) {
			m := Models{UNet: "model.gguf", LoRA: "model.safetensors", LoRAStrength: 0.5}
			p := Params{Prompt: "a wave", Seed: 108, Width: 957, Height: 543, Count: 3, Denoise: 0.7}
			textOnly := f.Workflow(m, p)
			textParams := p
			textParams.Denoise = 0
			if !reflect.DeepEqual(textOnly.API(), f.Workflow(m, textParams).API()) {
				t.Fatal("denoise changed a text-only workflow")
			}
			sam, isImage := imageFamilies[f.ID]
			if want := isImage || f.ID == "wan22-ti2v-5b"; f.SupportsInputImage() != want {
				t.Fatalf("SupportsInputImage() = %v; want %v", f.SupportsInputImage(), want)
			}
			if !f.SupportsInputImage() {
				return
			}
			g := f.workflow(m, p, filename)
			if _, err := g.UI("https://huggingface.co"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g.Models, textOnly.Models) {
				t.Errorf("input image changed the model files: %+v", g.Models)
			}
			byClass := map[string][]*Node{}
			for _, n := range g.Nodes {
				byClass[n.Class] = append(byClass[n.Class], n)
			}
			load := singleInputNode(t, byClass, "LoadImage")
			sampler := singleInputNode(t, byClass, "KSampler")
			if load.Inputs["image"] != filename || sampler.Inputs["seed"] != int64(108) || sampler.Inputs["steps"] != f.Defaults.Steps || sampler.Inputs["scheduler"] != "simple" {
				t.Fatalf("load %+v, sampler %+v", load.Inputs, sampler.Inputs)
			}
			if len(byClass["ManualSigmas"]) != 0 || len(byClass["EmptyLatentImage"]) != 0 || len(byClass["EmptySD3LatentImage"]) != 0 {
				t.Fatal("image workflow contains a text-only latent or fixed sigma schedule")
			}
			filled := f.Fill(p)
			if !isImage {
				wan := singleInputNode(t, byClass, "Wan22ImageToVideoLatent")
				if want := (map[string]any{"start_image": Link{Node: load.ID}, "width": filled.Width, "height": filled.Height,
					"length": filled.Frames, "batch_size": 2, "vae": Link{Node: singleInputNode(t, byClass, "VAELoader").ID}}); !reflect.DeepEqual(wan.Inputs, want) {
					t.Fatalf("Wan first-frame node %+v; want %+v", wan.Inputs, want)
				}
				if sampler.Inputs["latent_image"] != (Link{Node: wan.ID}) || sampler.Inputs["denoise"] != 1.0 {
					t.Fatalf("Wan sampler %+v", sampler.Inputs)
				}
				if len(byClass["SaveVideo"]) != 2 || len(byClass["LatentFromBatch"]) != 2 {
					t.Fatal("Wan batch did not produce separate video outputs")
				}
				for i, save := range byClass["SaveVideo"] {
					video := inputLinkNode(t, g, save, "video", "CreateVideo")
					decoded := inputLinkNode(t, g, video, "images", "VAEDecode")
					batch := inputLinkNode(t, g, decoded, "samples", "LatentFromBatch")
					if batch.Inputs["samples"] != (Link{Node: sampler.ID}) || batch.Inputs["batch_index"] != i || batch.Inputs["length"] != 1 {
						t.Fatalf("Wan video %d reads %+v", i, batch.Inputs)
					}
				}
				return
			}
			if sampler.Inputs["denoise"] != p.Denoise || sampler.Inputs["sampler_name"] != sam.name {
				t.Fatalf("image sampler %+v", sampler.Inputs)
			}
			repeat := inputLinkNode(t, g, sampler, "latent_image", "RepeatLatentBatch")
			if repeat.Inputs["amount"] != p.Count {
				t.Fatalf("repeat %+v", repeat.Inputs)
			}
			encode := inputLinkNode(t, g, repeat, "samples", "VAEEncode")
			scale := inputLinkNode(t, g, encode, "pixels", "ImageScale")
			vae := inputLinkNode(t, g, encode, "vae", "VAELoader")
			if want := (map[string]any{"image": Link{Node: load.ID}, "width": filled.Width, "height": filled.Height,
				"upscale_method": "lanczos", "crop": "center"}); !reflect.DeepEqual(scale.Inputs, want) {
				t.Fatalf("ImageScale %+v; want %+v", scale.Inputs, want)
			}
			decode := inputLinkNode(t, g, singleInputNode(t, byClass, "SaveImage"), "images", "VAEDecode")
			if vae.Inputs["vae_name"] != sam.vae || decode.Inputs["vae"] != encode.Inputs["vae"] || decode.Inputs["samples"] != (Link{Node: sampler.ID}) {
				t.Fatalf("wrong VAE/decode: %+v, %+v", vae.Inputs, decode.Inputs)
			}
		})
	}
}

func singleInputNode(t *testing.T, byClass map[string][]*Node, class string) *Node {
	t.Helper()
	if len(byClass[class]) != 1 {
		t.Fatalf("want one %s, got %d", class, len(byClass[class]))
	}
	return byClass[class][0]
}

func inputLinkNode(t *testing.T, g *Graph, from *Node, field, class string) *Node {
	t.Helper()
	link, ok := from.Inputs[field].(Link)
	if !ok || link.Slot != 0 {
		t.Fatalf("%s.%s is not a link to the first output of %s: %+v", from.Class, field, class, from.Inputs[field])
	}
	node := g.Node(link.Node)
	if node == nil || node.Class != class {
		t.Fatalf("%s.%s should link to %s, got %+v", from.Class, field, class, node)
	}
	return node
}
