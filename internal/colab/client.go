// Package colab talks to Colab: the runtimes of an account, which it
// assigns and unassigns, its compute units, and the runtime itself, through
// the proxy in front of it.
//
// The endpoints are those Google's own Colab extension for VS Code uses
// (github.com/googlecolab/colab-vscode), which take an OAuth token of a
// client that Colab lets in.
package colab

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
	"strconv"
	"strings"
	"time"
)

var (
	// PublicAPI manages runtimes.
	PublicAPI = "https://colaboratory.googleapis.com"
	// GapiAPI tells an account's compute units.
	GapiAPI = "https://colab.pa.googleapis.com"
)

const (
	clientAgentHeader = "X-Colab-Client-Agent"
	proxyTokenHeader  = "X-Colab-Runtime-Proxy-Token"
)

// ClientAgent is what requests say they come from. Colab knows the agents
// of its own clients; the VS Code extension's is the one that runs runtimes
// from outside the browser.
var ClientAgent = "vscode"

// Variant is the kind of machine.
type Variant string

const (
	CPU Variant = "VARIANT_CPU"
	GPU Variant = "VARIANT_GPU"
	TPU Variant = "VARIANT_TPU"
)

// Shape is the machine's memory.
type Shape string

const (
	Standard Shape = "SHAPE_STANDARD"
	HighMem  Shape = "SHAPE_HIGHMEM"
)

// Spec says what machine to assign.
type Spec struct {
	Variant     Variant `json:"variant"`
	Accelerator string  `json:"accelerator"`
	Shape       Shape   `json:"shape"`
}

func (s Spec) String() string {
	v := strings.TrimPrefix(string(s.Variant), "VARIANT_")
	if s.Accelerator != "" && s.Accelerator != "NONE" {
		v += " " + s.Accelerator
	}
	if s.Shape == HighMem {
		v += " High-RAM"
	}
	return v
}

// RuntimeSpec is a spec the account can or can't assign.
type RuntimeSpec struct {
	Key      Spec `json:"key"`
	Eligible bool `json:"eligible"`
}

// Connection is how to reach a runtime: its proxy's URL and the token the
// proxy wants.
type Connection struct {
	Endpoint   string    `json:"endpoint"`
	URL        string    `json:"url"`
	Token      string    `json:"token"`
	ExpireTime time.Time `json:"expireTime"`
}

// Runtime is an assigned machine.
type Runtime struct {
	Name       string      `json:"name"`
	Spec       Spec        `json:"runtimeSpec"`
	Connection *Connection `json:"connectionInfo"`
	Version    string      `json:"version,omitempty"`
}

// ID is the runtime's name without its "runtimes/" prefix.
func (r *Runtime) ID() string { return strings.TrimPrefix(r.Name, "runtimes/") }

// Accelerator is an accelerator kind with its models.
type Accelerator struct {
	Variant string   `json:"variant"`
	Models  []string `json:"models"`
}

// Quota is an account's compute units, as Colab tells them.
type Quota struct {
	Tier        string        `json:"subscriptionTier"`
	Balance     float64       `json:"paidComputeUnitsBalance"`
	RateHourly  float64       `json:"consumptionRateHourly"`
	Assignments int           `json:"assignmentsCount"`
	Eligible    []Accelerator `json:"eligibleAccelerators"`
	Ineligible  []Accelerator `json:"ineligibleAccelerators"`
	FreeCCU     *FreeQuota    `json:"freeCcuQuotaInfo"`
	FetchedAt   time.Time     `json:"-"`
}

// FreeQuota is the free tier's allowance.
type FreeQuota struct {
	RemainingTokens string `json:"remainingTokens"`
	NextRefill      int64  `json:"nextRefillTimestampSec"`
}

// FreeRemaining is the free tier's tokens left, -1 when Colab didn't say.
func (q *Quota) FreeRemaining() int64 {
	if q.FreeCCU == nil || q.FreeCCU.RemainingTokens == "" {
		return -1
	}
	n, err := strconv.ParseInt(q.FreeCCU.RemainingTokens, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// HoursLeft is how long the balance lasts at the current rate, -1 when
// nothing is being spent.
func (q *Quota) HoursLeft() float64 {
	if q.RateHourly <= 0 {
		return -1
	}
	return q.Balance / q.RateHourly
}

// Resources is what a runtime has and uses.
type Resources struct {
	Memory struct {
		Total int64 `json:"totalBytes"`
		Free  int64 `json:"freeBytes"`
	} `json:"memory"`
	Disks []struct {
		Filesystem struct {
			Label string `json:"label"`
			Total int64  `json:"totalBytes"`
			Used  int64  `json:"usedBytes"`
		} `json:"filesystem"`
	} `json:"disks"`
	GPUs []GPUInfo `json:"gpus"`
}

// GPUInfo is one of a runtime's GPUs.
type GPUInfo struct {
	Name        string  `json:"name"`
	MemoryUsed  int64   `json:"memoryUsedBytes"`
	MemoryTotal int64   `json:"memoryTotalBytes"`
	Utilization float64 `json:"gpuUtilization"`
}

// VRAM is the memory of the runtime's GPUs together.
func (r *Resources) VRAM() int64 {
	var n int64
	for _, g := range r.GPUs {
		n += g.MemoryTotal
	}
	return n
}

// Errors creating a runtime, as Colab gives their reasons.
var (
	ErrTooManyRuntimes = errors.New("colab: the account has too many runtimes")
	ErrQuota           = errors.New("colab: not enough compute units or usage quota")
	ErrUnavailable     = errors.New("colab: no such accelerator is available now")
	ErrDenylisted      = errors.New("colab: the account is blocked from Colab runtimes")
	ErrNotFound        = errors.New("colab: no such runtime")
	ErrUnauthorized    = errors.New("colab: Colab refused the account's token")
)

// APIError is an error response.
type APIError struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
	Reason  string
	Details []map[string]any `json:"details"`
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("colab: %d %s", e.Status, e.Message)
	if e.Reason != "" {
		s += " (" + e.Reason + ")"
	}
	return s
}

// Client calls Colab for one account.
type Client struct {
	// Token returns a fresh access token.
	Token func(ctx context.Context) (string, error)
	// Expired hears that Colab refused the token.
	Expired func()
	HTTP    *http.Client
	// AppName says which app calls, beside ClientAgent.
	AppName string
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set(clientAgentHeader, ClientAgent)
	if c.AppName != "" {
		req.Header.Set("X-Colab-VS-Code-App-Name", c.AppName)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		if c.Expired != nil {
			c.Expired()
		}
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return parseError(resp.StatusCode, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func parseError(status int, data []byte) error {
	var wrap struct {
		Error APIError `json:"error"`
	}
	if json.Unmarshal(data, &wrap) != nil || wrap.Error.Message == "" {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &APIError{Status: status, Message: msg}
	}
	e := wrap.Error
	e.Status = status
	e.Reason = reasonOf(e.Details)
	return &e
}

func reasonOf(details []map[string]any) string {
	for _, d := range details {
		if r, ok := d["reason"].(string); ok {
			return r
		}
	}
	return ""
}

// Quota returns the account's compute units and what it may assign.
func (c *Client) Quota(ctx context.Context) (*Quota, error) {
	var q Quota
	if err := c.do(ctx, "GET", GapiAPI+"/v1/user-info?get_ccu_consumption_info=true", nil, &q); err != nil {
		return nil, err
	}
	q.Tier = strings.TrimPrefix(q.Tier, "SUBSCRIPTION_TIER_")
	q.FetchedAt = time.Now()
	return &q, nil
}

// RuntimeSpecs returns the machines Colab offers the account.
func (c *Client) RuntimeSpecs(ctx context.Context) ([]RuntimeSpec, error) {
	var r struct {
		Specs []RuntimeSpec `json:"runtimeSpecs"`
	}
	if err := c.do(ctx, "GET", PublicAPI+"/v1beta/runtimespecs", nil, &r); err != nil {
		return nil, err
	}
	return r.Specs, nil
}

// Runtimes returns the account's runtimes.
func (c *Client) Runtimes(ctx context.Context) ([]Runtime, error) {
	var all []Runtime
	page := ""
	for {
		u := PublicAPI + "/v1beta/runtimes"
		if page != "" {
			u += "?pageToken=" + url.QueryEscape(page)
		}
		var r struct {
			Runtimes []Runtime `json:"runtimes"`
			Next     string    `json:"nextPageToken"`
		}
		if err := c.do(ctx, "GET", u, nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Runtimes...)
		if r.Next == "" {
			return all, nil
		}
		page = r.Next
	}
}

// Runtime returns one runtime, with a fresh connection.
func (c *Client) Runtime(ctx context.Context, id string) (*Runtime, error) {
	var r Runtime
	if err := c.do(ctx, "GET", PublicAPI+"/v1beta/runtimes/"+url.PathEscape(id), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

type operation struct {
	Name     string          `json:"name"`
	Done     bool            `json:"done"`
	Error    *opError        `json:"error"`
	Response json.RawMessage `json:"response"`
}

type opError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Details []map[string]any `json:"details"`
}

// CreateRuntime assigns a runtime and waits until it's ready.
func (c *Client) CreateRuntime(ctx context.Context, spec Spec) (*Runtime, error) {
	if spec.Accelerator == "" {
		spec.Accelerator = "NONE"
	}
	if spec.Shape == "" {
		spec.Shape = Standard
	}
	body := map[string]any{"runtimeSpec": spec}
	var op operation
	if err := c.do(ctx, "POST", PublicAPI+"/v1beta/runtimes?requestId="+requestID(), body, &op); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for !op.Done {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("colab: assigning %s took too long", spec)
		}
		id := strings.TrimPrefix(op.Name, "operations/")
		if err := c.do(ctx, "GET", PublicAPI+"/v1/operations/"+url.PathEscape(id)+":wait?timeout=60s", nil, &op); err != nil {
			return nil, err
		}
	}
	if op.Error != nil {
		return nil, opErr(op.Error, spec)
	}
	var r Runtime
	if err := json.Unmarshal(op.Response, &r); err != nil {
		return nil, err
	}
	if r.Connection == nil {
		// The operation's answer may leave it out; the runtime has it.
		return c.Runtime(ctx, r.ID())
	}
	return &r, nil
}

func opErr(e *opError, spec Spec) error {
	switch reasonOf(e.Details) {
	case "TOO_MANY_ACTIVE_RUNTIMES":
		return ErrTooManyRuntimes
	case "DENYLISTED":
		return ErrDenylisted
	case "QUOTA_EXCEEDED_USAGE_TIME":
		return ErrQuota
	}
	if e.Code == 9 && spec.Accelerator != "NONE" {
		return fmt.Errorf("%w: %s", ErrUnavailable, spec.Accelerator)
	}
	return &APIError{Code: e.Code, Message: e.Message, Reason: reasonOf(e.Details)}
}

// DeleteRuntime unassigns a runtime; one already gone is fine.
func (c *Client) DeleteRuntime(ctx context.Context, id string) error {
	err := c.do(ctx, "DELETE", PublicAPI+"/v1beta/runtimes/"+url.PathEscape(id)+"?allowMissing=true", nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Resources returns what the runtime has and uses.
func (c *Client) Resources(ctx context.Context, conn *Connection) (*Resources, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(conn.URL, "/")+"/api/colab/resources", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(proxyTokenHeader, conn.Token)
	req.Header.Set(clientAgentHeader, ClientAgent)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, parseError(resp.StatusCode, data)
	}
	var r Resources
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func requestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
