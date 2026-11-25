package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A reply streams piece by piece; one that stops sending fails after
// IdleTimeout instead of hanging, however long it streamed before.
func TestStreamIdle(t *testing.T) {
	stall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 5 {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
		if r.URL.Query().Get("stall") == "" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(stall)
	old := IdleTimeout
	IdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { IdleTimeout = old })

	var got strings.Builder
	if _, err := Stream(context.Background(), srv.URL, "", "m", nil, 0, func(c, _ string) { got.WriteString(c) }); err != nil || got.String() != "01234" {
		t.Errorf("got %q, %v", got.String(), err)
	}
	start := time.Now()
	_, err := Stream(context.Background(), srv.URL+"/?stall=1", "", "m", nil, 0, func(string, string) {})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("a stalled reply ended with %v after %s", err, time.Since(start))
	}
}
