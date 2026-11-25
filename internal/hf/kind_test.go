package hf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An image model packed as GGUF for ComfyUI is refused before a GPU is
// assigned for it, and a language model passes. Hugging Face's answer for
// abenzerps/Qwen-Image-2.1-Turbo-Quantized is the image one.
func TestCheckServable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models/a/image":
			w.Write([]byte(`{"gguf":{"architecture":"qwen_image"},"pipeline_tag":"text-to-image","tags":["comfyui-gguf"]}`))
		case "/api/models/a/llm":
			w.Write([]byte(`{"gguf":{"architecture":"qwen3"},"pipeline_tag":"text-generation"}`))
		case "/api/models/a/flux":
			w.Write([]byte(`{"gguf":{"architecture":"flux"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := Endpoint
	Endpoint = srv.URL
	defer func() { Endpoint = old }()
	oldClient := HTTP
	HTTP = srv.Client()
	defer func() { HTTP = oldClient }()

	for repo, refused := range map[string]bool{"a/image": true, "a/llm": false, "a/flux": true} {
		info, err := FetchInfo(context.Background(), repo, "")
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		if got := errors.Is(info.CheckServable(), ErrNotLLM); got != refused {
			t.Errorf("%s refused = %v, want %v", repo, got, refused)
		}
	}
	// Without an answer, as from a mirror, nothing is refused.
	var none *Info
	if none.CheckServable() != nil {
		t.Error("a missing answer refused the model")
	}
}
