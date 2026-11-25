// Package hf finds the GGUF files of a Hugging Face repository, and picks
// the best quantization that fits in a machine's memory.
package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Official is Hugging Face's own address, the only one tokens go to.
const Official = "https://huggingface.co"

// Endpoint is Hugging Face's address; mirrors such as hf-mirror.com work,
// over https, and get no token.
var Endpoint = Official

// CheckEndpoint returns the endpoint as requests use it, without a slash
// at the end, or an error for one that isn't an https address.
func CheckEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", fmt.Errorf("hf: %q isn't an address", endpoint)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("hf: the endpoint %q should be an https address", endpoint)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("hf: the endpoint %q should be a plain address", endpoint)
	}
	return endpoint, nil
}

// IsOfficial reports whether endpoint is Hugging Face's own, to which the
// token may go.
func IsOfficial(endpoint string) bool {
	e, err := CheckEndpoint(endpoint)
	if err != nil {
		return false
	}
	u, _ := url.Parse(e)
	return strings.EqualFold(u.Hostname(), "huggingface.co") && (u.Port() == "" || u.Port() == "443") && u.Path == ""
}

// TokenFor is the token to send to endpoint: none to mirrors, which could
// keep it.
func TokenFor(endpoint, token string) string {
	if !IsOfficial(endpoint) {
		return ""
	}
	return token
}

// HTTP is the client requests go through.
var HTTP = &http.Client{Timeout: 30 * time.Second}

// File is a file of a repository.
type File struct {
	Path string
	Size int64
}

// Ref is a repository, and the quantization asked for, if any, as in
// "unsloth/Qwen3-8B-GGUF:Q4_K_M".
type Ref struct {
	Repo  string
	Quant string
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][\w.\-]*/[\w.\-]+$`)

// ParseRef reads a repository as people write it: "user/repo",
// "user/repo:Q4_K_M", or its address on huggingface.co.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return Ref{}, fmt.Errorf("hf: %q names no repository", s)
		}
		s = parts[0] + "/" + parts[1]
	}
	s = strings.TrimPrefix(s, "hf.co/")
	var r Ref
	r.Repo, r.Quant, _ = strings.Cut(s, ":")
	if !repoPattern.MatchString(r.Repo) {
		return Ref{}, fmt.Errorf("hf: %q isn't a repository, as user/name", s)
	}
	r.Quant = strings.ToUpper(strings.TrimSpace(r.Quant))
	return r, nil
}

// ErrNotFound is returned for a repository that doesn't exist, or that
// needs a token to see.
var ErrNotFound = errors.New("hf: no such repository, or it needs an access token")

// ListFiles returns the files of a repository's main branch. The token
// goes only to Hugging Face itself, not to a mirror.
func ListFiles(ctx context.Context, repo, token string) ([]File, error) {
	endpoint, err := CheckEndpoint(Endpoint)
	if err != nil {
		return nil, err
	}
	token = TokenFor(endpoint, token)
	u := fmt.Sprintf("%s/api/models/%s/tree/main?recursive=true", endpoint, repo)
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
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404:
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("hf: %s", resp.Status)
	}
	var entries []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  *struct {
			Size int64 `json:"size"`
		} `json:"lfs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	var files []File
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		size := e.Size
		if e.LFS != nil && e.LFS.Size > 0 {
			size = e.LFS.Size
		}
		files = append(files, File{Path: e.Path, Size: size})
	}
	return files, nil
}

// DownloadURL is where a file of a repository downloads from.
func DownloadURL(repo, path string) string {
	return fmt.Sprintf("%s/%s/resolve/main/%s", strings.TrimSuffix(Endpoint, "/"), repo, path)
}

// ranks orders quantizations by how close they keep to the model: higher
// is better. Q8_0 comes before the 16-bit files, being nearly as good at
// half the size, and twice as fast.
var ranks = map[string]int{
	"Q8_0": 100, "BF16": 98, "F16": 97, "F32": 96, "Q8_K_XL": 99,
	"Q6_K_XL": 92, "Q6_K_L": 91, "Q6_K": 90,
	"Q5_K_XL": 87, "Q5_K_L": 86, "Q5_K_M": 85, "Q5_K_S": 83, "Q5_1": 82, "Q5_0": 81,
	"Q4_K_XL": 78, "Q4_K_L": 77, "Q4_K_M": 76, "MXFP4": 75, "Q4_K_S": 74, "IQ4_NL": 73,
	"IQ4_XS": 72, "Q4_1": 71, "Q4_0": 70,
	"Q3_K_XL": 64, "Q3_K_L": 63, "Q3_K_M": 62, "IQ3_M": 61, "IQ3_S": 60, "Q3_K_S": 59,
	"IQ3_XS": 58, "IQ3_XXS": 57,
	"Q2_K_XL": 50, "Q2_K_L": 49, "Q2_K": 48, "IQ2_M": 47, "IQ2_S": 46, "IQ2_XS": 45,
	"IQ2_XXS": 44, "TQ2_0": 43, "TQ1_0": 42, "IQ1_M": 41, "IQ1_S": 40,
}

var (
	quantPattern = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ[1-4]_(?:XXS|XS|NL|S|M)|Q[2-8]_K(?:_(?:XL|S|M|L))?|Q[4-8]_[01]|TQ[12]_0|MXFP4(?:_MOE)?|BF16|F16|F32))(?:[-_.]|$)`)
	splitPattern = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)
)

// QuantOf is the quantization a file's name says, upper case, "" for none.
func QuantOf(path string) string {
	name := path[strings.LastIndexByte(path, '/')+1:]
	name = strings.TrimSuffix(splitPattern.ReplaceAllString(name, ".gguf"), ".gguf")
	m := quantPattern.FindAllStringSubmatch(name+".", -1)
	if m == nil {
		return ""
	}
	q := strings.ToUpper(m[len(m)-1][1])
	q = strings.TrimPrefix(q, "UD-")
	q = strings.TrimSuffix(q, "_MOE")
	return q
}

// Quant is one quantization of a model: its file, or the parts it's split
// into.
type Quant struct {
	Name  string
	Files []string
	Size  int64
	Rank  int
}

// Quants groups a repository's GGUF files by quantization, best first.
// The projectors of multimodal models are left out.
func Quants(files []File) []Quant {
	byKey := map[string]*Quant{}
	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".gguf") || IsProjector(f.Path) {
			continue
		}
		q := QuantOf(f.Path)
		if q == "" {
			continue
		}
		// Different models in one repository keep apart by their folder
		// and their name without the part number.
		key := splitPattern.ReplaceAllString(f.Path, "")
		g := byKey[key]
		if g == nil {
			g = &Quant{Name: q, Rank: ranks[q]}
			byKey[key] = g
		}
		g.Files = append(g.Files, f.Path)
		g.Size += f.Size
	}
	out := make([]Quant, 0, len(byKey))
	for _, q := range byKey {
		sort.Strings(q.Files)
		out = append(out, *q)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].Size < out[j].Size
	})
	return out
}

// IsProjector reports whether a file is a multimodal projector.
func IsProjector(path string) bool {
	return strings.Contains(strings.ToLower(path[strings.LastIndexByte(path, '/')+1:]), "mmproj")
}

// Projector picks the projector of a multimodal model, "" for none: the
// 16-bit one if there is one, as llama.cpp's own downloads do.
func Projector(files []File) string {
	best, bestRank := "", -1
	for _, f := range files {
		if !IsProjector(f.Path) || !strings.HasSuffix(strings.ToLower(f.Path), ".gguf") {
			continue
		}
		r := 0
		switch QuantOf(f.Path) {
		case "F16", "BF16":
			r = 3
		case "Q8_0":
			r = 2
		case "F32":
			r = 1
		}
		if r > bestRank {
			best, bestRank = f.Path, r
		}
	}
	return best
}

// Need estimates the memory a model needs to run with a context of ctx
// tokens: its weights, the KV cache, which grows with the model, and the
// compute buffers.
func Need(size int64, ctx int) int64 {
	perToken := int64(64<<10) + size/100000
	return size + size/20 + int64(ctx)*perToken + 600<<20
}

// Choice is the quantization picked for a machine.
type Choice struct {
	Quant
	// Fits is false when the model fits only with some of its layers on
	// the CPU.
	Fits bool
}

// ErrTooBig is returned when no quantization fits at all.
var ErrTooBig = errors.New("hf: no quantization of the model fits in this machine's memory")

// Pick chooses the best quantization that fits in mem bytes of memory
// (the GPU's, or the machine's when there is none) with a context of ctx
// tokens. Failing that, it chooses one that fits in mem and spill, the
// machine's memory beside the GPU, with layers on the CPU. want picks a
// quantization by name instead.
func Pick(quants []Quant, mem, spill int64, ctx int, want string) (Choice, error) {
	if len(quants) == 0 {
		return Choice{}, errors.New("hf: the repository has no GGUF files")
	}
	if want != "" && want != "AUTO" {
		for _, q := range quants {
			if q.Name == want {
				return Choice{Quant: q, Fits: Need(q.Size, ctx) <= mem}, nil
			}
		}
		return Choice{}, fmt.Errorf("hf: the repository has no %s file", want)
	}
	for _, q := range quants {
		if Need(q.Size, ctx) <= mem {
			return Choice{Quant: q, Fits: true}, nil
		}
	}
	for _, q := range quants {
		if Need(q.Size, ctx) <= mem+spill {
			return Choice{Quant: q}, nil
		}
	}
	return Choice{}, ErrTooBig
}
