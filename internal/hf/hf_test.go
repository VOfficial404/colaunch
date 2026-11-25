package hf

import "testing"

const gib = 1 << 30

// Names as the big uploaders write them read as their quantization.
func TestQuantOf(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen3-8B-Q4_K_M.gguf":                        "Q4_K_M",
		"Meta-Llama-3.1-8B-Instruct-IQ4_XS.gguf":      "IQ4_XS",
		"gemma-3-27b-it-UD-Q4_K_XL.gguf":              "Q4_K_XL",
		"Q8_0/Llama-3.3-70B-Q8_0-00001-of-00002.gguf": "Q8_0",
		"model.bf16.gguf":                             "BF16",
		"gpt-oss-20b-MXFP4.gguf":                      "MXFP4",
		"DeepSeek-R1-UD-IQ1_S-00003-of-00004.gguf":    "IQ1_S",
		"phi-4-q5_k_s.gguf":                           "Q5_K_S",
		"tinyllama-1.1b-chat-v1.0.Q4_0.gguf":          "Q4_0",
		"README.gguf":                                 "",
	} {
		if got := QuantOf(name); got != want {
			t.Errorf("QuantOf(%q) = %q, want %q", name, got, want)
		}
	}
}

// Split parts add up into one quantization, and projectors stay out.
func TestQuantsGroupsParts(t *testing.T) {
	qs := Quants([]File{
		{Path: "Q8_0/m-Q8_0-00001-of-00002.gguf", Size: 40 * gib},
		{Path: "Q8_0/m-Q8_0-00002-of-00002.gguf", Size: 30 * gib},
		{Path: "m-Q4_K_M.gguf", Size: 40 * gib},
		{Path: "mmproj-m-f16.gguf", Size: 1 * gib},
		{Path: "README.md", Size: 10},
	})
	if len(qs) != 2 {
		t.Fatalf("got %d quants: %+v", len(qs), qs)
	}
	if qs[0].Name != "Q8_0" || len(qs[0].Files) != 2 || qs[0].Size != 70*gib {
		t.Errorf("first quant %+v", qs[0])
	}
}

// The best quantization that fits is picked: a T4's 15 GB takes an 8B
// model's Q8_0, and a 70B model's Q4_K_M needs an 80 GB A100.
func TestPick(t *testing.T) {
	small := Quants([]File{
		{Path: "m-Q8_0.gguf", Size: 8_500_000_000},
		{Path: "m-Q6_K.gguf", Size: 6_600_000_000},
		{Path: "m-Q4_K_M.gguf", Size: 4_900_000_000},
		{Path: "m-F16.gguf", Size: 16_000_000_000},
	})
	c, err := Pick(small, 15*gib, 12*gib, 8192, "")
	if err != nil || c.Name != "Q8_0" || !c.Fits {
		t.Errorf("8B on a T4: %+v, %v", c, err)
	}
	big := Quants([]File{
		{Path: "m-Q4_K_M-00001-of-00002.gguf", Size: 21_000_000_000},
		{Path: "m-Q4_K_M-00002-of-00002.gguf", Size: 21_500_000_000},
		{Path: "m-Q2_K.gguf", Size: 26_000_000_000},
	})
	if c, _ := Pick(big, 40*gib, 80*gib, 8192, ""); c.Name != "Q2_K" {
		t.Errorf("70B on a 40 GB A100 picked %s", c.Name)
	}
	if c, _ := Pick(big, 80*gib, 160*gib, 8192, ""); c.Name != "Q4_K_M" {
		t.Errorf("70B on an 80 GB A100 picked %s", c.Name)
	}
	if c, err := Pick(big, 15*gib, 40*gib, 8192, ""); err != nil || c.Fits {
		t.Errorf("70B on a T4 with spill: %+v %v", c, err)
	}
	if _, err := Pick(big, 4*gib, 4*gib, 8192, ""); err != ErrTooBig {
		t.Errorf("70B in 8 GB: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"unsloth/Qwen3-8B-GGUF":                             {"unsloth/Qwen3-8B-GGUF", ""},
		"unsloth/Qwen3-8B-GGUF:q4_k_m":                      {"unsloth/Qwen3-8B-GGUF", "Q4_K_M"},
		"https://huggingface.co/bartowski/x-GGUF/tree/main": {"bartowski/x-GGUF", ""},
		"hf.co/a/b:Q8_0":                                    {"a/b", "Q8_0"},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v", in, got, err)
		}
	}
	if _, err := ParseRef("not a repo"); err == nil {
		t.Error("ParseRef accepted a sentence")
	}
}
