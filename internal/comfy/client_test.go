package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeComfy answers as ComfyUI does: /prompt queues, /ws tells the
// progress, /history has the outputs once done, /view serves them.
type fakeComfy struct {
	t  *testing.T
	ws bool
	// refuse is /prompt's answer refusing the workflow, as ComfyUI's.
	refuse string
	// fail is the execution_error the run ends with.
	fail bool
	// hang is a run that never ends, to be interrupted.
	hang bool
	// outputs is the history's outputs.
	outputs    string
	deployment string
	upload     http.HandlerFunc

	mu          sync.Mutex
	prompt      map[string]any
	clientID    string
	id          string
	done        bool
	interrupted []string
	deleted     []string
	viewed      []string
}

func (fc *fakeComfy) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"system":{"comfyui_version":"0.46.0"},"devices":[]}`))
	})
	mux.HandleFunc("/userdata/colaunch.json", func(w http.ResponseWriter, r *http.Request) {
		if fc.deployment != "" {
			w.Write([]byte(fc.deployment))
			return
		}
		w.Write([]byte(`{"family":"wan21-t2v","unet":"colaunch/x__y/wan-Q4_K_M.gguf","lora":"colaunch/l__m/l.safetensors","lora_strength":0.8}`))
	})
	if fc.upload != nil {
		mux.HandleFunc("/upload/image", fc.upload)
	}
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt   map[string]any `json:"prompt"`
			ClientID string         `json:"client_id"`
			PromptID string         `json:"prompt_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if fc.refuse != "" {
			w.WriteHeader(400)
			w.Write([]byte(fc.refuse))
			return
		}
		fc.mu.Lock()
		fc.prompt, fc.clientID, fc.id = body.Prompt, body.ClientID, body.PromptID
		fc.mu.Unlock()
		if !fc.hang {
			go func() {
				time.Sleep(30 * time.Millisecond)
				fc.mu.Lock()
				fc.done = true
				fc.mu.Unlock()
			}()
		}
		json.NewEncoder(w).Encode(map[string]any{"prompt_id": body.PromptID, "number": 3, "node_errors": map[string]any{}})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if !fc.ws {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		send := func(typ string, data map[string]any) {
			b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
			conn.Write(ctx, websocket.MessageText, b)
		}
		send("status", map[string]any{"status": map[string]any{"exec_info": map[string]any{"queue_remaining": 0}}, "sid": r.URL.Query().Get("clientId")})
		var id, sampler string
		for id == "" {
			time.Sleep(5 * time.Millisecond)
			fc.mu.Lock()
			id = fc.id
			for nid, n := range fc.prompt {
				if n.(map[string]any)["class_type"] == "KSampler" {
					sampler = nid
				}
			}
			fc.mu.Unlock()
		}
		send("status", map[string]any{"status": map[string]any{"exec_info": map[string]any{"queue_remaining": 2}}})
		send("execution_start", map[string]any{"prompt_id": id})
		// Another client's prompt is no business of this one.
		send("execution_error", map[string]any{"prompt_id": "other", "node_id": "1", "exception_message": "not ours"})
		send("executing", map[string]any{"node": sampler, "prompt_id": id})
		conn.Write(ctx, websocket.MessageBinary, make([]byte, 100_000))
		for i := 1; i <= 3; i++ {
			send("progress", map[string]any{"value": i, "max": 3, "node": sampler, "prompt_id": id})
		}
		if fc.fail {
			send("execution_error", map[string]any{"prompt_id": id, "node_id": "4", "node_type": "VAELoader",
				"exception_type": "safetensors.SafetensorError", "exception_message": "Error while deserializing header: header too small\n"})
			return
		}
		if fc.hang {
			<-ctx.Done()
			return
		}
		for {
			fc.mu.Lock()
			done := fc.done
			fc.mu.Unlock()
			if done {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		send("executing", map[string]any{"node": nil, "prompt_id": id})
		<-ctx.Done()
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		fc.mu.Lock()
		done := fc.done && id == fc.id
		fc.mu.Unlock()
		switch {
		case fc.fail && id == fc.id:
			w.Write([]byte(`{"` + id + `":{"outputs":{},"status":{"status_str":"error","completed":false,"messages":[["execution_start",{}],["execution_error",{"node_id":"4","node_type":"VAELoader","exception_type":"SafetensorError","exception_message":"header too small"}]]}}}`))
		case !done:
			w.Write([]byte(`{}`))
		default:
			w.Write([]byte(`{"` + id + `":{"outputs":` + fc.outputs + `,"status":{"status_str":"success","completed":true,"messages":[]}}}`))
		}
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body struct{ Delete []string }
			json.NewDecoder(r.Body).Decode(&body)
			fc.mu.Lock()
			fc.deleted = append(fc.deleted, body.Delete...)
			fc.mu.Unlock()
			return
		}
		fc.mu.Lock()
		id := fc.id
		fc.mu.Unlock()
		w.Write([]byte(`{"queue_running":[[1,"a",{},{},[]]],"queue_pending":[[2,"b",{},{},[]],[3,"` + id + `",{},{},[]]]}`))
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("type") != "output" {
			http.NotFound(w, r)
			return
		}
		fc.mu.Lock()
		fc.viewed = append(fc.viewed, q.Get("subfolder")+"/"+q.Get("filename"))
		fc.mu.Unlock()
		w.Write([]byte("data of " + q.Get("filename")))
	})
	mux.HandleFunc("/interrupt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PromptID string `json:"prompt_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		fc.mu.Lock()
		fc.interrupted = append(fc.interrupted, body.PromptID)
		fc.mu.Unlock()
	})
	return httptest.NewServer(mux)
}

const videoOutputs = `{"11":{"images":[{"filename":"wan21_00001_.mp4","subfolder":"colaunch","type":"output"}],"animated":[true]},
	"12":{"images":[{"filename":"preview.png","subfolder":"","type":"temp"}]}}`

// With the WebSocket, the progress comes as ComfyUI sends it, the
// deployment's model and LoRA go in the workflow, and the video comes
// back as one.
func TestGenerateWebSocket(t *testing.T) {
	fc := &fakeComfy{t: t, ws: true, outputs: videoOutputs}
	srv := fc.server()
	defer srv.Close()
	c := New(srv.URL)
	if err := c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	var seen []Progress
	out, err := c.Generate(context.Background(), nil, Params{Prompt: "waves"}, func(p Progress) { seen = append(seen, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Filename != "wan21_00001_.mp4" || out[0].Kind != Video || out[0].MIME != "video/mp4" || string(out[0].Data) != "data of wan21_00001_.mp4" {
		t.Fatalf("outputs %+v", out)
	}
	if fc.clientID != c.id || fc.viewed[0] != "colaunch/wan21_00001_.mp4" || len(fc.viewed) != 1 {
		t.Errorf("client %q, viewed %v", fc.clientID, fc.viewed)
	}
	var unet, lora string
	var strength float64
	for _, n := range fc.prompt {
		in := n.(map[string]any)["inputs"].(map[string]any)
		if v, ok := in["unet_name"].(string); ok {
			unet = v
		}
		if v, ok := in["lora_name"].(string); ok {
			lora, strength = v, in["strength_model"].(float64)
		}
	}
	if unet != "colaunch/x__y/wan-Q4_K_M.gguf" || lora != "colaunch/l__m/l.safetensors" || strength != 0.8 {
		t.Errorf("unet %q, lora %q %v", unet, lora, strength)
	}
	last := seen[len(seen)-1]
	if last.Step != 3 || last.Steps != 3 || last.Node != "KSampler" {
		t.Errorf("progress %+v", seen)
	}
	queued := false
	for _, p := range seen {
		queued = queued || p.Queue == 1
	}
	if !queued {
		t.Errorf("no place in the queue: %+v", seen)
	}
}

// Without the WebSocket, the history is polled, and the queue tells the
// place.
func TestGeneratePolls(t *testing.T) {
	fc := &fakeComfy{t: t, outputs: `{"9":{"images":[{"filename":"a.png","subfolder":"colaunch","type":"output"},{"filename":"b.png","subfolder":"colaunch","type":"output"}]}}`}
	srv := fc.server()
	defer srv.Close()
	c := New(srv.URL)
	c.Poll = 10 * time.Millisecond
	out, err := c.Generate(context.Background(), FamilyByID("qwen-image"), Params{Prompt: "a cat", Count: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Kind != Image || out[0].MIME != "image/png" || out[1].Filename != "b.png" {
		t.Fatalf("outputs %+v", out)
	}
}

// ComfyUI refusing the workflow says which node and why.
func TestNodeErrors(t *testing.T) {
	fc := &fakeComfy{t: t, refuse: `{"error": {"type": "prompt_outputs_failed_validation", "message": "Prompt outputs failed validation", "details": "", "extra_info": {}}, "node_errors": {"8": {"errors": [{"type": "value_not_in_list", "message": "Value not in list", "details": "sampler_name: 'nope' not in (list of length 45)", "extra_info": {"input_name": "sampler_name", "input_config": null, "received_value": "nope"}}], "dependent_outputs": ["11"], "class_type": "KSampler"}}}`}
	srv := fc.server()
	defer srv.Close()
	_, err := New(srv.URL).Generate(context.Background(), nil, Params{Prompt: "x"}, nil)
	var pe *PromptError
	if !errors.As(err, &pe) || len(pe.Nodes) != 1 || pe.Nodes[0].Class != "KSampler" {
		t.Fatalf("%v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "node 8 (KSampler): Value not in list: sampler_name: 'nope'") || !strings.Contains(msg, "Prompt outputs failed validation") {
		t.Errorf("message %q", msg)
	}
	// A missing model, as ComfyUI says it for a bare error.
	fc.refuse = `{"error": {"type": "invalid_prompt", "message": "Cannot execute because a node is missing the class_type property.", "details": "Node ID '#3'", "extra_info": {}}, "node_errors": {}}`
	_, err = New(srv.URL).Generate(context.Background(), nil, Params{Prompt: "x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing the class_type property. (Node ID '#3')") {
		t.Errorf("%v", err)
	}
}

// A node failing as it runs is told, from the WebSocket or the history.
func TestExecutionError(t *testing.T) {
	for _, ws := range []bool{true, false} {
		fc := &fakeComfy{t: t, ws: ws, fail: true}
		srv := fc.server()
		c := New(srv.URL)
		c.Poll = 10 * time.Millisecond
		_, err := c.Generate(context.Background(), nil, Params{Prompt: "x"}, nil)
		var ee *ExecutionError
		if !errors.As(err, &ee) || ee.Node != "4" || ee.Class != "VAELoader" || !strings.Contains(err.Error(), "header too small") {
			t.Errorf("ws %v: %v", ws, err)
		}
		srv.Close()
	}
}

// Canceling interrupts the prompt and takes it off the queue.
func TestCancelInterrupts(t *testing.T) {
	fc := &fakeComfy{t: t, ws: true, hang: true}
	srv := fc.server()
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := New(srv.URL).Generate(ctx, nil, Params{Prompt: "x"}, func(p Progress) {
			if p.Step == 3 {
				cancel()
			}
		})
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Generate didn't return")
	}
	fc.mu.Lock()
	interrupted, deleted, id := fc.interrupted, fc.deleted, fc.id
	fc.mu.Unlock()
	if len(interrupted) != 1 || interrupted[0] != id || len(deleted) != 1 || deleted[0] != id {
		t.Errorf("interrupted %v, deleted %v", interrupted, deleted)
	}
	// Interrupt alone stops whatever runs.
	if err := New(srv.URL).Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.interrupted) != 2 || fc.interrupted[1] != "" {
		t.Errorf("interrupted %v", fc.interrupted)
	}
}

func TestOutputKinds(t *testing.T) {
	var outputs map[string]map[string]json.RawMessage
	json.Unmarshal([]byte(`{"1":{"images":[{"filename":"a.webp","subfolder":"","type":"output"}],"animated":[true]},
		"2":{"images":[{"filename":"b.webp","subfolder":"","type":"output"}]},
		"3":{"gifs":[{"filename":"c.mp4","subfolder":"","type":"output"}]}}`), &outputs)
	files := outputFiles(outputs)
	if len(files) != 3 || !files[0].animated || files[1].animated || !files[2].animated {
		t.Errorf("%+v", files)
	}
}
