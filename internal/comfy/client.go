package comfy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Output is a file a generation made.
type Output struct {
	Filename string
	Kind     Kind
	MIME     string
	Data     []byte
}

// Progress is where a generation is.
type Progress struct {
	// Step of Steps is the sampler's, 0 of 0 outside it.
	Step, Steps int
	// Node is what runs, as "KSampler".
	Node string
	// Queue is how many generations wait before this one.
	Queue   int
	Message string
}

// Client talks to a ComfyUI server's API.
type Client struct {
	base string
	// HTTP is the client requests go through.
	HTTP *http.Client
	// Poll is how often the history is read without the WebSocket, or
	// as a check beside it.
	Poll time.Duration
	// MaxOutput is the most bytes an output may have.
	MaxOutput int64

	id string
	mu sync.Mutex
	// deployment is what the plugin deployed, once read.
	deployment *Deployment
}

// New returns a client of the ComfyUI at baseURL, as
// http://127.0.0.1:8188.
func New(baseURL string) *Client {
	return &Client{
		base:      strings.TrimRight(baseURL, "/"),
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		Poll:      time.Second,
		MaxOutput: 2 << 30,
		id:        randomHex(16),
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// uuid4 is a random UUID, as ComfyUI takes for a prompt's ID.
func uuid4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (c *Client) do(ctx context.Context, method, p string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+p, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &statusError{code: resp.StatusCode, status: resp.Status, body: b}
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("comfy: %s %s: %w", method, p, err)
		}
	}
	return nil
}

type statusError struct {
	code   int
	status string
	body   []byte
}

func (e *statusError) Error() string {
	msg := strings.TrimSpace(string(e.body))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		return "comfy: " + e.status
	}
	return "comfy: " + e.status + ": " + msg
}

// Ready returns nil once ComfyUI answers.
func (c *Client) Ready(ctx context.Context) error {
	var stats struct {
		System map[string]any `json:"system"`
	}
	if err := c.do(ctx, "GET", "/system_stats", nil, &stats); err != nil {
		return err
	}
	if stats.System == nil {
		return errors.New("comfy: the server isn't ComfyUI")
	}
	return nil
}

// Interrupt stops what ComfyUI is running.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.do(ctx, "POST", "/interrupt", map[string]any{}, nil)
}

// Deployment is what the comfyui plugin deployed, as it writes it to
// ComfyUI's user folder (user/default/colaunch.json).
type Deployment struct {
	Family       string  `json:"family"`
	UNet         string  `json:"unet"`
	LoRA         string  `json:"lora,omitempty"`
	LoRAStrength float64 `json:"lora_strength,omitempty"`
	Model        string  `json:"model,omitempty"`
	Quant        string  `json:"quant,omitempty"`
}

// DeploymentFile is the deployment's file, in ComfyUI's user folder.
const DeploymentFile = "colaunch.json"

// Deployment reads what the comfyui plugin deployed.
func (c *Client) Deployment(ctx context.Context) (*Deployment, error) {
	c.mu.Lock()
	d := c.deployment
	c.mu.Unlock()
	if d != nil {
		return d, nil
	}
	d = &Deployment{}
	if err := c.do(ctx, "GET", "/userdata/"+url.PathEscape(DeploymentFile), nil, d); err != nil {
		return nil, err
	}
	if d.UNet == "" {
		return nil, errors.New("comfy: the deployment names no model")
	}
	c.mu.Lock()
	c.deployment = d
	c.mu.Unlock()
	return d, nil
}

// models finds the files f's workflow loads: the deployment's, or else
// the one GGUF ComfyUI has.
func (c *Client) models(ctx context.Context, f *Family) (*Family, Models, error) {
	d, err := c.Deployment(ctx)
	if err == nil {
		if f == nil {
			f = FamilyByID(d.Family)
		}
		if f == nil {
			return nil, Models{}, fmt.Errorf("comfy: the deployment's family %q is unknown", d.Family)
		}
		return f, Models{UNet: d.UNet, LoRA: d.LoRA, LoRAStrength: d.LoRAStrength}, nil
	}
	if f == nil {
		return nil, Models{}, fmt.Errorf("comfy: no family, and %w", err)
	}
	var info map[string]struct {
		Input struct {
			Required map[string][]json.RawMessage `json:"required"`
		} `json:"input"`
	}
	if err := c.do(ctx, "GET", "/object_info/UnetLoaderGGUF", nil, &info); err != nil {
		return nil, Models{}, err
	}
	var names []string
	if spec := info["UnetLoaderGGUF"].Input.Required["unet_name"]; len(spec) > 0 {
		json.Unmarshal(spec[0], &names)
	}
	if len(names) != 1 {
		return nil, Models{}, fmt.Errorf("comfy: ComfyUI has %d GGUF models and no deployment of Colaunch's to choose from", len(names))
	}
	return f, Models{UNet: names[0]}, nil
}

// NodeError is what ComfyUI found wrong with a node.
type NodeError struct {
	ID, Class, Title string
	Errors           []string
}

// PromptError is ComfyUI refusing a workflow.
type PromptError struct {
	Type, Message, Details string
	Nodes                  []NodeError
}

func (e *PromptError) Error() string {
	var b strings.Builder
	b.WriteString("ComfyUI refused the workflow: ")
	b.WriteString(e.Message)
	if e.Details != "" && !strings.Contains(e.Message, e.Details) {
		b.WriteString(" (" + e.Details + ")")
	}
	for _, n := range e.Nodes {
		name := n.Class
		if n.Title != "" && n.Title != n.Class {
			name = n.Title + ", " + n.Class
		}
		fmt.Fprintf(&b, "\n- node %s (%s): %s", n.ID, name, strings.Join(n.Errors, "; "))
	}
	return b.String()
}

// promptError reads ComfyUI's answer refusing a workflow.
func promptError(body []byte, g *Graph) error {
	var r struct {
		Error json.RawMessage `json:"error"`
		Nodes map[string]struct {
			Class  string `json:"class_type"`
			Errors []struct {
				Type, Message, Details string
				Extra                  struct {
					Input string `json:"input_name"`
				} `json:"extra_info"`
			} `json:"errors"`
		} `json:"node_errors"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	e := &PromptError{}
	var top struct{ Type, Message, Details string }
	if json.Unmarshal(r.Error, &top) == nil {
		e.Type, e.Message, e.Details = top.Type, top.Message, top.Details
	} else {
		json.Unmarshal(r.Error, &e.Message)
	}
	ids := make([]string, 0, len(r.Nodes))
	for id := range r.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := r.Nodes[id]
		ne := NodeError{ID: id, Class: n.Class}
		if g != nil {
			if node := g.Node(id); node != nil {
				ne.Title = node.Title
			}
		}
		for _, x := range n.Errors {
			msg := x.Message
			if x.Details != "" {
				msg += ": " + x.Details
			}
			ne.Errors = append(ne.Errors, msg)
		}
		e.Nodes = append(e.Nodes, ne)
	}
	if e.Message == "" && len(e.Nodes) == 0 {
		return nil
	}
	if e.Message == "" {
		e.Message = "the workflow has errors"
	}
	return e
}

// ExecutionError is a node failing as the workflow runs.
type ExecutionError struct {
	Node, Class, Type, Message string
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("ComfyUI failed at node %s (%s): %s: %s", e.Node, e.Class, e.Type, strings.TrimSpace(e.Message))
}

// ErrInterrupted is returned for a generation stopped in ComfyUI.
var ErrInterrupted = errors.New("comfy: the generation was interrupted")

// Submit queues a workflow, returning its prompt's ID.
func (c *Client) Submit(ctx context.Context, g *Graph) (string, error) {
	id := uuid4()
	var r struct {
		PromptID string  `json:"prompt_id"`
		Number   float64 `json:"number"`
	}
	err := c.do(ctx, "POST", "/prompt", map[string]any{"prompt": g.API(), "client_id": c.id, "prompt_id": id}, &r)
	var se *statusError
	if errors.As(err, &se) {
		if pe := promptError(se.body, g); pe != nil {
			return "", pe
		}
	}
	if err != nil {
		return "", err
	}
	if r.PromptID == "" {
		return "", errors.New("comfy: ComfyUI gave no prompt ID")
	}
	return r.PromptID, nil
}

type event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// listen reads ComfyUI's events for this client until ctx is done.
func (c *Client) listen(ctx context.Context) (<-chan event, error) {
	u, err := url.Parse(c.base + "/ws")
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.RawQuery = url.Values{"clientId": {c.id}}.Encode()
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// The WebSocket lives longer than HTTP.Timeout: it ends with ctx.
	conn, _, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: &http.Client{Transport: c.HTTP.Transport}})
	if err != nil {
		return nil, err
	}
	// Previews of the latent come as binary messages.
	conn.SetReadLimit(64 << 20)
	ch := make(chan event, 64)
	go func() {
		defer close(ch)
		defer conn.CloseNow()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var e event
			if json.Unmarshal(data, &e) != nil {
				continue
			}
			select {
			case ch <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// Generate runs f's workflow for p, and returns what it made. progress,
// if not nil, is told where it is. Canceling ctx interrupts ComfyUI.
// f may be nil for the deployment's family.
func (c *Client) Generate(ctx context.Context, f *Family, p Params, progress func(Progress)) ([]Output, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	f, m, err := c.models(ctx, f)
	if err != nil {
		return nil, err
	}
	return c.Run(ctx, f.Workflow(m, p), progress)
}

// Run queues a workflow and waits for what it makes, as Generate does.
func (c *Client) Run(ctx context.Context, g *Graph, progress func(Progress)) ([]Output, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	// Listen before queueing, not to miss the events.
	events, wsErr := c.listen(wctx)
	id, err := c.Submit(ctx, g)
	if err != nil {
		return nil, err
	}
	progress(Progress{Message: "queued"})
	out, err := c.wait(ctx, g, id, events, wsErr == nil, progress)
	if err != nil && ctx.Err() != nil {
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.do(ictx, "POST", "/queue", map[string]any{"delete": []string{id}}, nil)
		c.do(ictx, "POST", "/interrupt", map[string]any{"prompt_id": id}, nil)
		return nil, ctx.Err()
	}
	return out, err
}

func (c *Client) wait(ctx context.Context, g *Graph, id string, events <-chan event, ws bool, progress func(Progress)) ([]Output, error) {
	poll := c.Poll
	if ws {
		poll = 5 * c.Poll
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	cur := Progress{Message: "queued"}
	nodeName := func(id string) string {
		if n := g.Node(id); n != nil {
			if n.Title != "" {
				return n.Title
			}
			return n.Class
		}
		return id
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e, ok := <-events:
			if !ok {
				events = nil
				tick.Reset(c.Poll)
				continue
			}
			var d struct {
				PromptID string  `json:"prompt_id"`
				Node     *string `json:"node"`
				Value    int     `json:"value"`
				Max      int     `json:"max"`
				Status   struct {
					Exec struct {
						Queue int `json:"queue_remaining"`
					} `json:"exec_info"`
				} `json:"status"`
				NodeID  string `json:"node_id"`
				Type    string `json:"node_type"`
				Message string `json:"exception_message"`
				ExcType string `json:"exception_type"`
			}
			json.Unmarshal(e.Data, &d)
			if d.PromptID != "" && d.PromptID != id {
				continue
			}
			switch e.Type {
			case "status":
				// What remains counts this one, while it waits.
				if q := d.Status.Exec.Queue - 1; cur.Message == "queued" && q >= 0 && q != cur.Queue {
					cur.Queue = q
					progress(cur)
				}
			case "execution_start":
				cur = Progress{Message: "running"}
				progress(cur)
			case "executing":
				if d.Node == nil {
					if out, done, err := c.result(ctx, id); done {
						return out, err
					}
					continue
				}
				cur.Node, cur.Step, cur.Steps, cur.Message = nodeName(*d.Node), 0, 0, "running"
				progress(cur)
			case "progress":
				cur.Step, cur.Steps = d.Value, d.Max
				if d.Node != nil {
					cur.Node = nodeName(*d.Node)
				}
				progress(cur)
			case "execution_error":
				return nil, &ExecutionError{Node: d.NodeID, Class: d.Type, Type: d.ExcType, Message: d.Message}
			case "execution_interrupted":
				return nil, ErrInterrupted
			case "execution_success":
				if out, done, err := c.result(ctx, id); done {
					return out, err
				}
			}
		case <-tick.C:
			if out, done, err := c.result(ctx, id); done {
				return out, err
			}
			if events == nil {
				if q, ok := c.position(ctx, id); ok && q != cur.Queue {
					cur.Queue = q
					progress(cur)
				}
			}
		}
	}
}

// position is how many prompts wait before id, ok false once it runs.
func (c *Client) position(ctx context.Context, id string) (int, bool) {
	var q struct {
		Running [][]json.RawMessage `json:"queue_running"`
		Pending [][]json.RawMessage `json:"queue_pending"`
	}
	if c.do(ctx, "GET", "/queue", nil, &q) != nil {
		return 0, false
	}
	type item struct {
		n  float64
		id string
	}
	var mine *item
	var items []item
	for _, list := range [][][]json.RawMessage{q.Running, q.Pending} {
		for _, it := range list {
			if len(it) < 2 {
				continue
			}
			var x item
			json.Unmarshal(it[0], &x.n)
			json.Unmarshal(it[1], &x.id)
			items = append(items, x)
			if x.id == id {
				v := x
				mine = &v
			}
		}
	}
	if mine == nil {
		return 0, false
	}
	n := 0
	for _, it := range items {
		if it.id != id && it.n < mine.n {
			n++
		}
	}
	return n, true
}

// result reads the prompt's history: done is false while it hasn't
// ended.
func (c *Client) result(ctx context.Context, id string) (out []Output, done bool, err error) {
	var h map[string]struct {
		Outputs map[string]map[string]json.RawMessage `json:"outputs"`
		Status  struct {
			Status    string              `json:"status_str"`
			Completed bool                `json:"completed"`
			Messages  [][]json.RawMessage `json:"messages"`
		} `json:"status"`
	}
	if err := c.do(ctx, "GET", "/history/"+url.PathEscape(id), nil, &h); err != nil {
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		return nil, false, nil
	}
	entry, ok := h[id]
	if !ok {
		return nil, false, nil
	}
	if entry.Status.Status == "error" {
		for _, m := range entry.Status.Messages {
			if len(m) < 2 {
				continue
			}
			var typ string
			json.Unmarshal(m[0], &typ)
			switch typ {
			case "execution_error":
				var d struct {
					NodeID  string `json:"node_id"`
					Type    string `json:"node_type"`
					Message string `json:"exception_message"`
					ExcType string `json:"exception_type"`
				}
				json.Unmarshal(m[1], &d)
				return nil, true, &ExecutionError{Node: d.NodeID, Class: d.Type, Type: d.ExcType, Message: d.Message}
			case "execution_interrupted":
				return nil, true, ErrInterrupted
			}
		}
		return nil, true, errors.New("comfy: the generation failed")
	}
	if !entry.Status.Completed && entry.Status.Status != "success" {
		return nil, false, nil
	}
	files := outputFiles(entry.Outputs)
	if len(files) == 0 {
		return nil, true, errors.New("comfy: the generation made nothing")
	}
	for _, f := range files {
		o, err := c.download(ctx, f)
		if err != nil {
			return nil, true, err
		}
		out = append(out, o)
	}
	return out, true, nil
}

type outputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
	animated  bool
}

// outputFiles are the files a prompt's outputs name, by node, saved ones
// only: not the previews of the "temp" folder.
func outputFiles(outputs map[string]map[string]json.RawMessage) []outputFile {
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var files []outputFile
	for _, id := range ids {
		o := outputs[id]
		var animated []bool
		json.Unmarshal(o["animated"], &animated)
		anim := len(animated) > 0 && animated[0]
		for _, key := range []string{"images", "gifs", "videos", "video"} {
			var list []outputFile
			if json.Unmarshal(o[key], &list) != nil {
				continue
			}
			for _, f := range list {
				if f.Filename == "" || f.Type == "temp" {
					continue
				}
				f.animated = anim || key != "images"
				files = append(files, f)
			}
		}
	}
	return files
}

var mimes = map[string]string{
	".png": "image/png", ".webp": "image/webp", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".mkv": "video/x-matroska",
}

func (c *Client) download(ctx context.Context, f outputFile) (Output, error) {
	typ := f.Type
	if typ == "" {
		typ = "output"
	}
	q := url.Values{"filename": {f.Filename}, "subfolder": {f.Subfolder}, "type": {typ}}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/view?"+q.Encode(), nil)
	if err != nil {
		return Output{}, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Output{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Output{}, fmt.Errorf("comfy: %s: %s", f.Filename, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxOutput+1))
	if err != nil {
		return Output{}, err
	}
	if int64(len(data)) > c.MaxOutput {
		return Output{}, fmt.Errorf("comfy: %s is bigger than %d bytes", f.Filename, c.MaxOutput)
	}
	ext := strings.ToLower(path.Ext(f.Filename))
	mime := mimes[ext]
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	kind := Image
	if strings.HasPrefix(mime, "video/") || f.animated && (ext == ".webp" || ext == ".gif" || ext == ".png") {
		kind = Video
	}
	return Output{Filename: path.Base(f.Filename), Kind: kind, MIME: mime, Data: data}, nil
}
