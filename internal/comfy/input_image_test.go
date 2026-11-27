package comfy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testInputPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 8, 12))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestReadInputImage(t *testing.T) {
	pngData := testInputPNG(t)
	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, image.NewRGBA(image.Rect(0, 0, 8, 12)), nil); err != nil {
		t.Fatal(err)
	}
	jpegData := jpegBuf.Bytes()
	// A JPEG APP segment can contain an end marker inside its metadata.
	jpegWithMetadata := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, 6, 'x', 0xff, 0xd9, 'y'}, jpegData[2:]...)
	webpData, err := base64.StdEncoding.DecodeString("UklGRlQAAABXRUJQVlA4WAoAAAAQAAAAAwAAAwAAQUxQSAoAAAABB1DQiAhERP8DVlA4ICQAAABwAQCdASoEAAQAAUAmJZACdAF1AAD++nkuIP/O34g/UFyAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		ext  string
	}{
		{"PNG", pngData, ".png"},
		{"JPEG", jpegWithMetadata, ".jpg"},
		{"WebP", webpData, ".webp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			picture, err := ReadInputImage(bytes.NewReader(tc.data), "source.dat")
			if err != nil || picture.Filename != "source.dat" || !bytes.Equal(picture.Data, tc.data) {
				t.Fatalf("ReadInputImage: name %q, bytes %d, error %v", picture.Filename, len(picture.Data), err)
			}
			if ext, err := inputImageExtension(picture.Data); err != nil || ext != tc.ext {
				t.Fatalf("content extension %q, %v; want %q", ext, err, tc.ext)
			}
			if _, err := ReadInputImage(bytes.NewReader(tc.data[:len(tc.data)/2]), "source"+tc.ext); err == nil {
				t.Error("accepted truncated data")
			}
		})
	}

	oversizePNG := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(oversizePNG[16:20], 6000)
	binary.BigEndian.PutUint32(oversizePNG[20:24], 5000)
	binary.BigEndian.PutUint32(oversizePNG[29:33], crc32.ChecksumIEEE(oversizePNG[12:29]))
	animatedPNG := append(bytes.Clone(pngData[:33]), 0, 0, 0, 8, 'a', 'c', 'T', 'L', 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(animatedPNG[49:53], crc32.ChecksumIEEE(animatedPNG[37:49]))
	animatedPNG = append(animatedPNG, pngData[33:]...)
	animatedWebP, err := base64.StdEncoding.DecodeString("UklGRogAAABXRUJQVlA4WAoAAAASAAAAAwAAAwAAQU5JTQYAAAAAAAAAAABBTk1GKgAAAAAAAAAAAAMAAAMAAGQAAAJWUDhMEQAAAC8DwAAQB1CKLtWjoIGI6H8AAEFOTUYqAAAAAAAAAAAAAwAAAwAAZAAAAlZQOEwRAAAALwPAABAHUKEWFK96gYjofwAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		msg  string
	}{
		{"renamed text", []byte("not an image"), "must be PNG"},
		{"oversized body", make([]byte, maxInputImageBytes+1), "exceeds 20 MiB"},
		{"oversized dimensions", oversizePNG, "6000×5000"},
		{"APNG", animatedPNG, "single, complete PNG"},
		{"animated WebP", animatedWebP, "single, complete WebP"},
		{"concatenated JPEG", bytes.Join([][]byte{jpegData, jpegData}, nil), "single, complete JPEG"},
		{"trailing WebP", append(bytes.Clone(webpData), 0), "single, complete WebP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadInputImage(bytes.NewReader(tc.data), "source.png"); err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("got %v; want %q", err, tc.msg)
			}
		})
	}
}

func TestGenerateWithImage(t *testing.T) {
	data := testInputPNG(t)
	picture, err := ReadInputImage(bytes.NewReader(data), `C:\Users\artist\Pictures\original.jpg`)
	if err != nil {
		t.Fatal(err)
	}
	names := make(chan string, 2)
	uploadName := regexp.MustCompile(`^colaunch-[0-9a-f]{32}\.png$`)
	fc := &fakeComfy{
		t:          t,
		deployment: `{"family":"qwen-image-2.1-turbo","unet":"turbo.gguf"}`,
		outputs:    `{"12":{"images":[{"filename":"result.png","subfolder":"colaunch","type":"output"}]}}`,
		upload: func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.FormValue("type") != "input" || r.FormValue("overwrite") != "false" || r.FormValue("subfolder") != "" {
				t.Errorf("unexpected upload %s %v", r.Method, r.Form)
			}
			file, header, err := r.FormFile("image")
			if err != nil {
				t.Errorf("read multipart image: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, data) || !uploadName.MatchString(header.Filename) {
				t.Errorf("unexpected uploaded image %q (%d bytes): %v", header.Filename, len(got), err)
			}
			names <- header.Filename
			json.NewEncoder(w).Encode(map[string]any{"name": header.Filename, "type": "input", "subfolder": ""})
		},
	}
	srv := fc.server()
	defer srv.Close()
	c := New(srv.URL)
	c.Poll = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var previousName string
	for _, denoise := range []float64{0, 0.8} {
		var queued bool
		out, err := c.GenerateWithImage(ctx, nil, Params{Prompt: "a wave", Denoise: denoise}, picture, func(p Progress) {
			queued = queued || p.Message == "queued"
		})
		if err != nil || len(out) != 1 || out[0].Filename != "result.png" || !queued {
			t.Fatalf("GenerateWithImage: outputs %+v, queued %v, error %v", out, queued, err)
		}
		var name string
		select {
		case name = <-names:
		default:
			t.Fatal("no upload")
		}
		if name == previousName {
			t.Fatalf("reused upload name %q", name)
		}
		previousName = name
		fc.mu.Lock()
		prompt := fc.prompt
		fc.mu.Unlock()
		var load, sampler map[string]any
		for _, raw := range prompt {
			node := raw.(map[string]any)
			switch node["class_type"] {
			case "LoadImage":
				load = node["inputs"].(map[string]any)
			case "KSampler":
				sampler = node["inputs"].(map[string]any)
			}
		}
		want := denoise
		if want == 0 {
			want = DefaultDenoise
		}
		if load["image"] != name || sampler["denoise"] != want || sampler["steps"] != 8.0 {
			t.Fatalf("queued image %+v and sampler %+v; want name %q, denoise %v, 8 steps", load, sampler, name, want)
		}
	}
}

func TestImageUploadRejectsBadResponses(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed upload redirect to another HTTP target")
	}))
	defer other.Close()
	picture := InputImage{Data: testInputPNG(t)}
	for _, tc := range []struct {
		name      string
		reply     func(http.ResponseWriter, *http.Request, string)
		wantError string
	}{
		{"other HTTP target", func(w http.ResponseWriter, r *http.Request, _ string) {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
		}, "HTTP 307"},
		{"same HTTP target", func(w http.ResponseWriter, r *http.Request, _ string) {
			http.Redirect(w, r, "/redirected", http.StatusPermanentRedirect)
		}, "HTTP 308"},
		{"traversal name", func(w http.ResponseWriter, _ *http.Request, _ string) {
			io.WriteString(w, `{"name":"..\\image.png","type":"input","subfolder":""}`)
		}, "invalid input image upload response"},
		{"traversal folder", func(w http.ResponseWriter, _ *http.Request, name string) {
			json.NewEncoder(w).Encode(map[string]string{"name": name, "type": "input", "subfolder": "../outside"})
		}, "invalid input image upload response"},
		{"other name", func(w http.ResponseWriter, _ *http.Request, _ string) {
			io.WriteString(w, `{"name":"another.png","type":"input","subfolder":""}`)
		}, "invalid input image upload response"},
		{"other type", func(w http.ResponseWriter, _ *http.Request, name string) {
			json.NewEncoder(w).Encode(map[string]string{"name": name, "type": "output", "subfolder": ""})
		}, "invalid input image upload response"},
		{"missing subfolder", func(w http.ResponseWriter, _ *http.Request, name string) {
			json.NewEncoder(w).Encode(map[string]string{"name": name, "type": "input"})
		}, "invalid input image upload response"},
		{"invalid JSON", func(w http.ResponseWriter, _ *http.Request, _ string) {
			io.WriteString(w, `{"name":`)
		}, "invalid input image upload response"},
		{"oversized reply", func(w http.ResponseWriter, _ *http.Request, _ string) {
			io.WriteString(w, strings.Repeat(" ", maxUploadResponse+1))
		}, "invalid input image upload response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/userdata/colaunch.json":
					io.WriteString(w, `{"family":"qwen-image","unet":"qwen.gguf"}`)
				case "/upload/image":
					file, header, err := r.FormFile("image")
					if err != nil {
						t.Errorf("read upload: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					file.Close()
					tc.reply(w, r, header.Filename)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			_, err := New(srv.URL).GenerateWithImage(context.Background(), nil, Params{}, picture, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("got %v; want %q", err, tc.wantError)
			}
		})
	}
}

func TestGenerateWithImageRejectsBeforeUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userdata/colaunch.json" {
			io.WriteString(w, `{"family":"wan21-t2v","unet":"wan.gguf"}`)
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	defer srv.Close()
	picture := InputImage{Filename: "example.png", Data: testInputPNG(t)}
	imageFamily := FamilyByID("qwen-image")
	for _, tc := range []struct {
		base    string
		family  *Family
		picture InputImage
		denoise float64
		want    string
	}{
		{"http://example.com:8188", imageFamily, picture, 0, "local HTTP forward"},
		{"http://localhost:8188", imageFamily, picture, 0, "local HTTP forward"},
		{"https://127.0.0.1:8188", imageFamily, picture, 0, "local HTTP forward"},
		{srv.URL + "/other", imageFamily, picture, 0, "local HTTP forward"},
		{srv.URL + "?route=other", imageFamily, picture, 0, "local HTTP forward"},
		{srv.URL, nil, picture, 0, "does not support an input image"},
		{srv.URL, FamilyByID("wan21-t2v"), picture, 0, "does not support an input image"},
		{srv.URL, imageFamily, InputImage{Filename: "fake.png", Data: []byte("fake")}, 0, "must be PNG"},
		{srv.URL, imageFamily, picture, math.NaN(), "must be between 0.1 and 1"},
		{srv.URL, imageFamily, picture, 1.01, "must be between 0.1 and 1"},
	} {
		_, err := New(tc.base).GenerateWithImage(context.Background(), tc.family, Params{Denoise: tc.denoise}, tc.picture, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("target %s, denoise %v: got %v; want %q", tc.base, tc.denoise, err, tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(srv.URL).GenerateWithImage(ctx, imageFamily, Params{}, picture, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled generation returned %v", err)
	}
}
