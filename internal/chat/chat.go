// Package chat talks to an OpenAI-compatible API, as llama-server's,
// streaming the replies.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Client is what Stream sends with. A reply streams for as long as the
// model writes, so there is no limit on the whole request, but connecting
// and the answer's headers have one each: a server that doesn't answer
// fails instead of hanging. Cancel Stream's context to stop.
var Client = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	},
}

// IdleTimeout ends a reply that sends nothing for this long.
var IdleTimeout = 5 * time.Minute

// Message is a message of a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Reasoning is what a thinking model thought, shown apart.
	Reasoning string `json:"-"`
}

// Stats are what a reply took.
type Stats struct {
	PromptTokens, CompletionTokens int
	TokensPerSecond                float64
}

// Stream sends the conversation and calls delta with each piece of the
// reply as it comes.
func Stream(ctx context.Context, base, key, model string, msgs []Message, temperature float64, delta func(content, reasoning string)) (*Stats, error) {
	body, _ := json.Marshal(map[string]any{
		"model":             model,
		"messages":          msgs,
		"stream":            true,
		"temperature":       temperature,
		"stream_options":    map[string]any{"include_usage": true},
		"timings_per_token": false,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(base, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Silence for IdleTimeout ends the reply: the timer is pushed back
	// with each line.
	var silent atomic.Bool
	idle := time.AfterFunc(IdleTimeout, func() { silent.Store(true); cancel() })
	defer idle.Stop()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	stats := &Stats{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		idle.Reset(IdleTimeout)
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			Timings *struct {
				PredictedPerSecond float64 `json:"predicted_per_second"`
			} `json:"timings"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Error != nil {
			return stats, fmt.Errorf("%s", ev.Error.Message)
		}
		for _, c := range ev.Choices {
			if c.Delta.Content != "" || c.Delta.Reasoning != "" {
				delta(c.Delta.Content, c.Delta.Reasoning)
			}
		}
		if ev.Usage != nil {
			stats.PromptTokens, stats.CompletionTokens = ev.Usage.Prompt, ev.Usage.Completion
		}
		if ev.Timings != nil && ev.Timings.PredictedPerSecond > 0 {
			stats.TokensPerSecond = ev.Timings.PredictedPerSecond
		}
	}
	if err := sc.Err(); err != nil {
		if silent.Load() {
			return stats, fmt.Errorf("chat: nothing came for %s", IdleTimeout)
		}
		return stats, err
	}
	return stats, nil
}
