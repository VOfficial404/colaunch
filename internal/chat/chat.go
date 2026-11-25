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
	"net/http"
	"strings"
)

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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	stats := &Stats{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
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
	return stats, sc.Err()
}
