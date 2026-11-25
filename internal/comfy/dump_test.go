package comfy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDumpWorkflows writes every family's workflows, and the files they
// load, to $COLAUNCH_DUMP_WORKFLOWS, for
// scripts/validate_comfy_workflows.py to post to a real ComfyUI. It's
// skipped otherwise.
func TestDumpWorkflows(t *testing.T) {
	dir := os.Getenv("COLAUNCH_DUMP_WORKFLOWS")
	if dir == "" {
		t.Skip("COLAUNCH_DUMP_WORKFLOWS isn't set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		b, ok := v.([]byte)
		if !ok {
			var err error
			if b, err = json.MarshalIndent(v, "", " "); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	widgets := map[string][]string{}
	for class, spec := range nodeTypes {
		widgets[class] = spec.widgets
	}
	write("widgets.json", widgets)
	for _, f := range families {
		for _, lora := range []bool{false, true} {
			m := Models{UNet: "colaunch/test__" + f.ID + "/" + f.ID + "-Q4_K_M.gguf"}
			name := f.ID
			if lora {
				m.LoRA, name = "colaunch/test__lora/"+f.ID+"-lora.safetensors", f.ID+"-lora"
			}
			p := f.Defaults
			p.Prompt = "a lighthouse on a cliff at dawn"
			g := f.Workflow(m, p)
			files := []map[string]string{{"dir": "diffusion_models", "name": m.UNet}}
			if lora {
				files = append(files, map[string]string{"dir": "loras", "name": m.LoRA})
			}
			for _, c := range f.Files {
				files = append(files, map[string]string{"dir": c.Dir, "name": c.Name()})
			}
			ui, err := g.UI("https://huggingface.co")
			if err != nil {
				t.Fatal(err)
			}
			write(name+".api.json", g.API())
			write(name+".ui.json", ui)
			write(name+".files.json", files)
		}
	}
}
