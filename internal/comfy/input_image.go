package comfy

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"

	"golang.org/x/image/webp"
)

const (
	// DefaultDenoise is the image-to-image strength used when Params.Denoise is zero.
	DefaultDenoise = 0.55

	maxInputImageBytes  = 20 << 20
	maxInputImagePixels = 24_000_000
	maxInputImageSide   = 16_384
	maxUploadResponse   = 64 << 10
)

// InputImage holds the original bytes and display filename of a selected image.
// The filename is local metadata. ComfyUI receives a new, random filename.
type InputImage struct {
	Filename string
	Data     []byte
}

// ReadInputImage reads and validates a single PNG, JPEG, or WebP image. It
// takes at most 20 MiB and allows up to 24 megapixels and 16,384 pixels per side.
func ReadInputImage(r io.Reader, filename string) (InputImage, error) {
	if r == nil {
		return InputImage{}, errors.New("comfy: no input image reader")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxInputImageBytes+1))
	if err != nil {
		return InputImage{}, fmt.Errorf("comfy: read input image: %w", err)
	}
	if _, err := inputImageExtension(data); err != nil {
		return InputImage{}, err
	}
	return InputImage{Filename: filename, Data: data}, nil
}

func inputImageExtension(data []byte) (string, error) {
	if len(data) > maxInputImageBytes {
		return "", errors.New("comfy: input image exceeds 20 MiB")
	}
	var ext string
	var decodeConfig func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch http.DetectContentType(data) {
	case "image/png":
		ext, decodeConfig, decode = ".png", png.DecodeConfig, png.Decode
		if !singlePNG(data) {
			return "", errors.New("comfy: input must be a single, complete PNG image")
		}
	case "image/jpeg":
		ext, decodeConfig, decode = ".jpg", jpeg.DecodeConfig, jpeg.Decode
	case "image/webp":
		ext, decodeConfig, decode = ".webp", webp.DecodeConfig, webp.Decode
		if !singleWebP(data) {
			return "", errors.New("comfy: input must be a single, complete WebP image")
		}
	default:
		return "", errors.New("comfy: input image must be PNG, JPEG, or WebP")
	}
	config, err := decodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("comfy: read input image header: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxInputImageSide || config.Height > maxInputImageSide ||
		config.Width*config.Height > maxInputImagePixels {
		return "", fmt.Errorf("comfy: input image dimensions %d×%d exceed the image budget", config.Width, config.Height)
	}

	var jpegReader *jpegInputReader
	r := io.Reader(bytes.NewReader(data))
	if ext == ".jpg" {
		jpegReader = &jpegInputReader{remaining: data}
		r = jpegReader
	}
	img, err := decode(r)
	if err != nil {
		return "", fmt.Errorf("comfy: decode input image: %w", err)
	}
	if jpegReader != nil && len(jpegReader.remaining) != 0 {
		return "", errors.New("comfy: input must be a single, complete JPEG image")
	}
	if size := img.Bounds().Size(); size.X != config.Width || size.Y != config.Height {
		return "", errors.New("comfy: input image dimensions do not match its header")
	}
	return ext, nil
}

// LoadImage can read multiple frames. Keep animated PNGs out of still workflows.
func singlePNG(data []byte) bool {
	for chunks := data[8:]; len(chunks) >= 12; {
		size := binary.BigEndian.Uint32(chunks[:4])
		if uint64(size)+12 > uint64(len(chunks)) {
			return false
		}
		switch string(chunks[4:8]) {
		case "acTL", "fcTL", "fdAT":
			return false
		case "IEND":
			return size == 0 && len(chunks) == 12
		}
		chunks = chunks[int(size)+12:]
	}
	return false
}

// Decode stops after the primary WebP bitstream. Check the entire RIFF file
// for animation, extra bitstreams, and missing or trailing bytes.
func singleWebP(data []byte) bool {
	if len(data) < 12 || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return false
	}
	images := 0
	chunks := data[12:]
	for len(chunks) >= 8 {
		size := binary.LittleEndian.Uint32(chunks[4:8])
		end := 8 + uint64(size) + uint64(size%2)
		if end > uint64(len(chunks)) {
			return false
		}
		switch string(chunks[:4]) {
		case "ANIM", "ANMF":
			return false
		case "VP8X":
			if len(chunks) != len(data)-12 || size != 10 || chunks[8]&0x02 != 0 {
				return false
			}
		case "VP8 ", "VP8L":
			images++
		}
		chunks = chunks[int(end):]
	}
	return images == 1 && len(chunks) == 0
}

// Stop each read at a possible end marker so the JPEG decoder cannot read
// ahead into an appended image. It can read again if the marker was metadata.
type jpegInputReader struct {
	remaining  []byte
	previousFF bool
}

func (r *jpegInputReader) Read(p []byte) (int, error) {
	if len(r.remaining) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	chunk := r.remaining[:min(len(r.remaining), len(p))]
	if r.previousFF && chunk[0] == 0xd9 {
		chunk = chunk[:1]
	} else if i := bytes.Index(chunk, []byte{0xff, 0xd9}); i >= 0 {
		chunk = chunk[:i+2]
	}
	r.previousFF = chunk[len(chunk)-1] == 0xff
	r.remaining = r.remaining[len(chunk):]
	return copy(p, chunk), nil
}

// GenerateWithImage uploads an original still image through the local ComfyUI
// forward and runs f's image-to-image or first-frame video workflow. A nil f
// uses the deployed family. InputImage is validated again before any upload.
func (c *Client) GenerateWithImage(ctx context.Context, f *Family, p Params, image InputImage, progress func(Progress)) ([]Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f != nil && !f.SupportsInputImage() {
		return nil, fmt.Errorf("comfy: %s does not support an input image", f.ID)
	}
	endpoint, err := c.imageUploadURL()
	if err != nil {
		return nil, err
	}
	ext, err := inputImageExtension(image.Data)
	if err != nil {
		return nil, err
	}
	f, m, err := c.models(ctx, f)
	if err != nil {
		return nil, err
	}
	if !f.SupportsInputImage() {
		return nil, fmt.Errorf("comfy: %s does not support an input image", f.ID)
	}
	if f.Kind == Image && p.Denoise != 0 && (math.IsNaN(p.Denoise) || p.Denoise < 0.1 || p.Denoise > 1) {
		return nil, errors.New("comfy: image denoise must be between 0.1 and 1")
	}
	name, err := c.uploadImage(ctx, endpoint, "colaunch-"+randomHex(16)+ext, image.Data)
	if err != nil {
		return nil, err
	}
	return c.Run(ctx, f.workflow(m, p, name), progress)
}

func (c *Client) imageUploadURL() (string, error) {
	invalid := errors.New("comfy: input image uploads require a local HTTP forward")
	u, err := url.Parse(c.base)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid
	}
	ip, ipErr := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if ipErr != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || port == 0 {
		return "", invalid
	}
	u.Path = "/upload/image"
	return u.String(), nil
}

func (c *Client) uploadImage(ctx context.Context, endpoint, name string, data []byte) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("type", "input"); err != nil {
		return "", err
	}
	if err := form.WriteField("overwrite", "false"); err != nil {
		return "", err
	}
	part, err := form.CreateFormFile("image", name)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	// Send the private body directly to the selected forward, with neither
	// proxy routing nor redirect replay. Other ComfyUI requests use c.HTTP.
	client := http.Client{
		Timeout:   c.HTTP.Timeout,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("comfy: upload input image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("comfy: upload input image: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Name      string  `json:"name"`
		Type      string  `json:"type"`
		Subfolder *string `json:"subfolder"`
	}
	response, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadResponse+1))
	if err != nil {
		return "", fmt.Errorf("comfy: read input image upload response: %w", err)
	}
	// Only our exact name in the input directory may become a LoadImage path.
	if len(response) > maxUploadResponse || json.Unmarshal(response, &result) != nil ||
		result.Name != name || result.Type != "input" || result.Subfolder == nil || *result.Subfolder != "" {
		return "", errors.New("comfy: invalid input image upload response")
	}
	return result.Name, nil
}
