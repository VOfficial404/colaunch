// Package cloudflare is the little of Cloudflare's v4 API that publishes a
// runtime's port on the user's own domain: a remotely managed tunnel
// (cfd_tunnel), its ingress, a proxied CNAME to it, and optionally a
// Cloudflare Access application that lets in only listed emails.
//
// Every endpoint below was checked against the API reference at
// https://developers.cloudflare.com/api/ (the pages' Markdown, "…/index.md")
// on 2026-11-26. Each call notes its page.
package cloudflare

import (
	"bytes"
	"context"
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

// DefaultBase is the API's address, which tests point elsewhere.
var DefaultBase = "https://api.cloudflare.com/client/v4"

// Client calls the API with an API token.
type Client struct {
	Token string
	// Base is DefaultBase unless a test points it elsewhere.
	Base string
	HTTP *http.Client
}

// New is a client for token.
func New(token string) *Client {
	return &Client{Token: strings.TrimSpace(token), Base: DefaultBase, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// The permissions each part needs, as the dashboard's token form names
// them.
const (
	PermTunnel = "Account · Cloudflare Tunnel · Edit"
	PermDNS    = "Zone · DNS · Edit"
	PermZone   = "Zone · Zone · Read"
	PermAccess = "Account · Access: Apps and Policies · Edit"
)

// What can go wrong, for errors.Is.
var (
	// ErrToken is a token Cloudflare doesn't take: mistyped, expired or
	// disabled.
	ErrToken = errors.New("cloudflare: the API token isn't valid, or has expired")
	// ErrPermission is a token without a permission a call needs.
	ErrPermission = errors.New("cloudflare: the API token lacks a permission")
	// ErrNotFound is an account or domain the token can't see: it isn't
	// in the account, or the token isn't scoped to it.
	ErrNotFound = errors.New("cloudflare: not found in this account")
	// ErrConflict is a DNS record or Access application of that name that
	// Colaunch didn't make, which it leaves alone.
	ErrConflict = errors.New("cloudflare: something else already uses this hostname")
)

// Message is one of the errors or messages of a response.
type Message struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// envelope is every v4 response: success, errors, messages, result, and
// result_info on lists.
type envelope struct {
	Success    bool            `json:"success"`
	Errors     []Message       `json:"errors"`
	Messages   []Message       `json:"messages"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		TotalPages int `json:"total_pages"`
		Count      int `json:"count"`
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

// APIError is a call Cloudflare refused, with what it said, and what the
// call was for.
type APIError struct {
	// Op is what was being done, as "create the tunnel".
	Op     string
	Status int
	Errors []Message
	// Perm is the permission the call needs.
	Perm string
	kind error
}

func (e *APIError) Error() string {
	var parts []string
	for _, m := range e.Errors {
		parts = append(parts, fmt.Sprintf("%s (%d)", m.Message, m.Code))
	}
	said := strings.Join(parts, "; ")
	if said == "" {
		said = fmt.Sprintf("HTTP %d", e.Status)
	}
	switch e.kind {
	case ErrPermission:
		return fmt.Sprintf("Cloudflare won't let the API token %s: give it the permission %q. Cloudflare said: %s", e.Op, e.Perm, said)
	case ErrToken:
		return "Cloudflare doesn't accept the API token: it's mistyped, expired or disabled. Cloudflare said: " + said
	case ErrNotFound:
		return fmt.Sprintf("Cloudflare can't find what's needed to %s: the domain may not be in this account, or the token isn't scoped to it. Cloudflare said: %s", e.Op, said)
	}
	return fmt.Sprintf("Cloudflare: couldn't %s: %s", e.Op, said)
}

// Is matches the kind of error.
func (e *APIError) Is(target error) bool { return e.kind != nil && target == e.kind }

// Codes Cloudflare answers with. They aren't listed in the API reference;
// these are what the API returns for them in practice.
var (
	// 9106/9109: missing or invalid token; 6003/6111: invalid request
	// headers (a malformed token); 1000: invalid API token.
	tokenCodes = map[int]bool{1000: true, 6003: true, 6111: true, 9106: true, 9109: true}
	// 10000: authentication error, which a valid token without the
	// permission gets; 9103/10001: unauthorized for the resource.
	permCodes = map[int]bool{10000: true, 10001: true, 9103: true}
	// 7003/7000: no route for the URI (an unknown account or zone ID);
	// 1001/1003: invalid zone identifier.
	notFoundCodes = map[int]bool{7000: true, 7003: true, 1001: true, 1003: true}
)

func classify(status int, errs []Message) error {
	for _, m := range errs {
		switch {
		case tokenCodes[m.Code]:
			return ErrToken
		case permCodes[m.Code]:
			return ErrPermission
		case notFoundCodes[m.Code]:
			return ErrNotFound
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return ErrToken
	case http.StatusForbidden:
		return ErrPermission
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// do sends a request and decodes its result into out, if any.
func (c *Client) do(ctx context.Context, op, perm, method, path string, q url.Values, body, out any) (*envelope, error) {
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	u := strings.TrimRight(base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %s: %w", op, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 300 {
			e := &APIError{Op: op, Status: resp.StatusCode, Perm: perm}
			e.kind = classify(resp.StatusCode, nil)
			return nil, e
		}
		return nil, fmt.Errorf("cloudflare: %s: unreadable answer: %w", op, err)
	}
	if resp.StatusCode >= 300 || !env.Success {
		e := &APIError{Op: op, Status: resp.StatusCode, Errors: env.Errors, Perm: perm}
		e.kind = classify(resp.StatusCode, env.Errors)
		return &env, e
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return &env, fmt.Errorf("cloudflare: %s: %w", op, err)
		}
	}
	return &env, nil
}

// list reads every page of a list, up to maxPages of perPage.
func list[T any](ctx context.Context, c *Client, op, perm, path string, q url.Values) ([]T, error) {
	const perPage, maxPages = 50, 40
	if q == nil {
		q = url.Values{}
	}
	var all []T
	for page := 1; page <= maxPages; page++ {
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(perPage))
		var items []T
		env, err := c.do(ctx, op, perm, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if env.ResultInfo == nil || env.ResultInfo.TotalPages <= page || len(items) == 0 {
			break
		}
	}
	return all, nil
}

// Account is a Cloudflare account the token reaches.
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Zone is a domain of an account.
type Zone struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Account Account `json:"account"`
}

// Verify checks the token, and returns the accounts it reaches.
//
// A user's token verifies at GET /user/tokens/verify, an account's at
// GET /accounts/{account_id}/tokens/verify; both answer result.status
// "active", "disabled" or "expired" (API reference: User › Tokens › Verify
// Token, Accounts › Tokens › Verify Token).
func (c *Client) Verify(ctx context.Context) ([]Account, error) {
	var tok struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_, userErr := c.do(ctx, "verify the token", "", http.MethodGet, "/user/tokens/verify", nil, nil, &tok)
	if userErr == nil && tok.Status != "" && tok.Status != "active" {
		return nil, fmt.Errorf("%w (its status is %q)", ErrToken, tok.Status)
	}
	accounts, err := c.Accounts(ctx)
	if err != nil {
		if userErr != nil {
			return nil, userErr
		}
		return nil, err
	}
	if userErr != nil {
		// Perhaps an account's token, which the user endpoint doesn't
		// know.
		ok := false
		for _, a := range accounts {
			tok.Status = ""
			if _, err := c.do(ctx, "verify the token", "", http.MethodGet, "/accounts/"+url.PathEscape(a.ID)+"/tokens/verify", nil, nil, &tok); err == nil && (tok.Status == "" || tok.Status == "active") {
				ok = true
				break
			}
		}
		if !ok {
			return nil, userErr
		}
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("%w: the token reaches no account; give it the permission %q", ErrPermission, PermTunnel)
	}
	return accounts, nil
}

// Accounts lists the accounts the token reaches: GET /accounts, result
// [{id, name, …}] (API reference: Accounts › List Accounts).
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return list[Account](ctx, c, "list your accounts", PermTunnel, "/accounts", nil)
}

// Zones lists an account's domains: GET /zones?account.id=…, result
// [{id, name, status, account: {id, name}, …}]. The reference names "Zone
// Zone Read" as the permission it accepts (API reference: Zones › List
// Zones).
func (c *Client) Zones(ctx context.Context, accountID string) ([]Zone, error) {
	q := url.Values{"account.id": {accountID}}
	return list[Zone](ctx, c, "list the account's domains", PermZone, "/zones", q)
}
