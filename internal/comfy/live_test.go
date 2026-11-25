package comfy

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLive runs small workflows that need no model on a real ComfyUI at
// $COLAUNCH_COMFY_URL, as http://127.0.0.1:8199: an image and a video
// saved by ComfyUI's own nodes come back through the client. It's
// skipped otherwise.
func TestLive(t *testing.T) {
	base := os.Getenv("COLAUNCH_COMFY_URL")
	if base == "" {
		t.Skip("COLAUNCH_COMFY_URL isn't set")
	}
	c := New(base)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	image := &Graph{}
	img := image.add("EmptyImage", "", map[string]any{"width": 64, "height": 48, "batch_size": 2, "color": 0xff8800})
	image.add("SaveImage", "", map[string]any{"images": img, "filename_prefix": "colaunch/live"})

	video := &Graph{}
	frames := video.add("EmptyImage", "", map[string]any{"width": 64, "height": 48, "batch_size": 9, "color": 0x0088ff})
	v := video.add("CreateVideo", "", map[string]any{"images": frames, "fps": 8.0})
	video.add("SaveVideo", "", map[string]any{"video": v, "filename_prefix": "colaunch/live", "format": "mp4", "format.codec": "h264"})

	for name, tc := range map[string]struct {
		g    *Graph
		n    int
		kind Kind
		mime string
	}{"image": {image, 2, Image, "image/png"}, "video": {video, 1, Video, "video/mp4"}} {
		var steps []Progress
		out, err := c.Run(ctx, tc.g, func(p Progress) { steps = append(steps, p) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(out) != tc.n {
			t.Fatalf("%s: %d outputs", name, len(out))
		}
		for _, o := range out {
			if o.Kind != tc.kind || o.MIME != tc.mime || len(o.Data) < 100 {
				t.Errorf("%s: %s %s %s, %d bytes", name, o.Filename, o.Kind, o.MIME, len(o.Data))
			}
			t.Logf("%s: %s %s %d bytes; progress %+v", name, o.Filename, o.MIME, len(o.Data), steps)
		}
	}
}
