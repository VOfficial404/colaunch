package hf

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The token goes to huggingface.co alone: a mirror doesn't get it, and an
// endpoint that isn't https isn't asked at all.
func TestTokenGoesToHuggingFaceAlone(t *testing.T) {
	oldEndpoint, oldHTTP := Endpoint, HTTP
	t.Cleanup(func() { Endpoint, HTTP = oldEndpoint, oldHTTP })
	var got []*http.Request
	HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		got = append(got, r)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"type":"file","path":"m-Q4_K_M.gguf","size":5}]`)), Header: http.Header{}}, nil
	})}
	for endpoint, wantAuth := range map[string]bool{
		"https://huggingface.co":       true,
		"https://huggingface.co/":      true,
		"https://hf-mirror.com":        false,
		"https://huggingface.co.evil":  false,
		"https://evil.com/huggingface": false,
		"https://huggingface.co:8443":  false,
	} {
		got = nil
		Endpoint = endpoint
		files, err := ListFiles(context.Background(), "a/b", "hf_secret")
		if err != nil || len(files) != 1 || len(got) != 1 {
			t.Errorf("%s: %v, %d requests", endpoint, err, len(got))
			continue
		}
		if auth := got[0].Header.Get("Authorization"); (auth == "Bearer hf_secret") != wantAuth {
			t.Errorf("%s: Authorization %q", endpoint, auth)
		}
		if strings.Contains(got[0].URL.String(), "//api") {
			t.Errorf("%s: asked %s", endpoint, got[0].URL)
		}
	}
	for _, endpoint := range []string{"http://hf-mirror.com", "ftp://huggingface.co", "huggingface.co", "https://user:pw@huggingface.co"} {
		got = nil
		Endpoint = endpoint
		if _, err := ListFiles(context.Background(), "a/b", "hf_secret"); err == nil || len(got) != 0 {
			t.Errorf("%s: %v, %d requests", endpoint, err, len(got))
		}
	}
}

func TestIsOfficial(t *testing.T) {
	if !IsOfficial(Official) || !IsOfficial("https://HuggingFace.co/") || IsOfficial("http://huggingface.co") || IsOfficial("https://huggingface.co/mirror") {
		t.Error("IsOfficial")
	}
	if TokenFor("https://hf-mirror.com", "t") != "" || TokenFor(Official, "t") != "t" {
		t.Error("TokenFor")
	}
}
