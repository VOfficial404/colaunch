package colab

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTTYUpgradeErrors(t *testing.T) {
	const token = "fixture-token+/?&"
	var redirected atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer dest.Close()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/runtime/colab/tty" || r.URL.RawQuery != "" || r.Header.Get(proxyTokenHeader) != token || r.Header.Get(clientAgentHeader) != ClientAgent {
					t.Error("unexpected terminal WebSocket request")
				}
				w.Header().Set("Location", dest.URL)
				http.Error(w, token, status)
			}))
			defer proxy.Close()
			conn := &Connection{URL: proxy.URL + "/runtime/?token=ignored", Token: token}
			_, err := dialTTY(t.Context(), conn, proxy.Client())
			var api *APIError
			if !errors.As(err, &api) || api.Status != status || api.Message != http.StatusText(status) || strings.Contains(err.Error(), token) {
				t.Fatalf("unexpected WebSocket upgrade error: %v", err)
			}
			if count := redirected.Load(); count != 0 {
				t.Fatalf("redirect destination received %d requests", count)
			}
		})
	}
}

func TestProxyWebSocketRejectsInsecureURLs(t *testing.T) {
	for _, endpoint := range []string{sshPath, ttyPath} {
		for _, conn := range []*Connection{
			nil,
			{URL: "http://example.test"},
			{URL: "http://127.0.0.1", Token: "runtime-credentials"},
			{URL: "http://localhost.example.test", Token: "secret"},
			{URL: "http://localhost@example.test"},
			{URL: "ws://127.0.0.1"},
			{URL: "https://:443"},
		} {
			if _, err := proxyWebSocketURL(conn, endpoint); err == nil {
				t.Errorf("accepted insecure URL for %s", endpoint)
			}
		}
	}
	for _, conn := range []Connection{{URL: "http://localhost"}, {URL: "http://127.0.0.1", Token: "secret"}} {
		if u, err := proxyWebSocketURL(&conn, ttyPath); err != nil || u.Scheme != "ws" {
			t.Fatalf("loopback fixture URL: %v, %v", u, err)
		}
	}
}

func testShell() *Shell {
	s := &Shell{}
	s.cond = sync.NewCond(&s.bmu)
	return s
}

// A marker is found however the terminal's output is cut, and only a whole
// one: digits followed by "@@".
func TestFindMarkerAcrossChunks(t *testing.T) {
	out := "noise @@END-1 @@END-12:x@@ @@START-12@@\nhello\n@@END-12:3@@tail"
	for size := 1; size <= len(out); size++ {
		s := testShell()
		m := []byte("@@END-12:")
		found := -1
		for i := 0; i < len(out) && found < 0; i += size {
			s.buf.WriteString(out[i:min(i+size, len(out))])
			if start, end, code := s.find(m, true); start >= 0 {
				found = start
				if code != 3 || out[end:] != "tail" {
					t.Fatalf("chunks of %d: code %d, rest %q", size, code, out[end:])
				}
			}
		}
		if found != strings.Index(out, "@@END-12:3") {
			t.Fatalf("chunks of %d: found at %d", size, found)
		}
	}
}

// Output of 256 KiB arriving 400 bytes at a time, the waiting command
// looking after each piece.
const benchOut, benchChunk = 256 << 10, 400

func benchData() []byte {
	var b bytes.Buffer
	b.WriteString("@@START-7@@\n")
	for b.Len() < benchOut {
		b.WriteString("llama_model_loader: - tensor  123: blk.4.ffn_down.weight q4_K [4096, 11008]\n")
	}
	b.WriteString("@@END-7:0@@")
	return b.Bytes()
}

// What wait did before: run the regular expression over all the output
// after each piece.
func BenchmarkWaitRegexp(b *testing.B) {
	data := benchData()
	re := regexp.MustCompile(fmt.Sprintf(`@@START-%d@@\n([\s\S]*?)@@END-%d:(\d+)@@`, 7, 7))
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var buf bytes.Buffer
		for i := 0; i < len(data); i += benchChunk {
			buf.Write(data[i:min(i+benchChunk, len(data))])
			if m := re.FindSubmatchIndex(buf.Bytes()); m != nil {
				break
			}
		}
	}
}

// What wait does: look for the end marker in the new bytes only.
func BenchmarkWaitIncremental(b *testing.B) {
	data := benchData()
	m := []byte("@@END-7:")
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		s := testShell()
		for i := 0; i < len(data); i += benchChunk {
			s.buf.Write(data[i:min(i+benchChunk, len(data))])
			if start, _, _ := s.find(m, true); start >= 0 {
				bytes.LastIndex(s.buf.Bytes()[:start], []byte("@@START-7@@\n"))
				break
			}
		}
	}
}
