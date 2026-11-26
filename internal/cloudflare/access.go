package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// AccessApp is a Cloudflare Access application.
type AccessApp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Type     string `json:"type"`
	Policies []struct {
		ID string `json:"id"`
	} `json:"policies"`
}

// AccessPolicy is a reusable Access policy.
type AccessPolicy struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Decision string `json:"decision"`
}

// accessApps lists the account's Access applications, filtered by q.
//
// GET /accounts/{account_id}/access/apps?name=…&exact=true or ?domain=…,
// result [{id, name, domain, type, policies, …}] (API reference: Zero
// Trust › Access › Applications › List Access applications).
func (c *Client) accessApps(ctx context.Context, accountID string, q url.Values) ([]AccessApp, error) {
	return list[AccessApp](ctx, c, "read the Access applications", PermAccess, "/accounts/"+url.PathEscape(accountID)+"/access/apps", q)
}

// accessPolicy is the account's reusable policy named name, nil for none.
//
// GET /accounts/{account_id}/access/policies, result [{id, name, decision,
// …}] (API reference: Zero Trust › Access › Policies › List Access
// reusable policies).
func (c *Client) accessPolicy(ctx context.Context, accountID, name string) (*AccessPolicy, error) {
	ps, err := list[AccessPolicy](ctx, c, "read the Access policies", PermAccess, "/accounts/"+url.PathEscape(accountID)+"/access/policies", nil)
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i], nil
		}
	}
	return nil, nil
}

// cleanEmails trims the emails, drops empty ones and repeats.
func cleanEmails(emails []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// ensureAccess puts host behind Access, letting in only emails: a reusable
// policy named name that allows them, and a self-hosted application named
// name for host that uses it. Returns the application's ID.
//
// The policy: POST /accounts/{account_id}/access/policies, or PUT
// …/access/policies/{policy_id}, body {name, decision: "allow", include:
// [{email: {email}}, …]} (API reference: Create / Update an Access
// reusable policy).
//
// The application: POST /accounts/{account_id}/access/apps, or PUT
// …/access/apps/{app_id}, body {name, domain, type: "self_hosted",
// session_duration, policies: [{id, precedence}]} (API reference: Add /
// Update an Access application).
func (c *Client) ensureAccess(ctx context.Context, accountID, host, name string, emails []string) (string, error) {
	emails = cleanEmails(emails)
	if len(emails) == 0 {
		return "", errors.New("cloudflare: Access needs at least one email")
	}
	// An application Colaunch didn't make already guards the hostname:
	// leave it, and say so.
	others, err := c.accessApps(ctx, accountID, url.Values{"domain": {host}})
	if err != nil {
		return "", err
	}
	for _, a := range others {
		if a.Name != name && strings.EqualFold(strings.TrimRight(a.Domain, "/"), host) {
			return "", &ConflictError{Hostname: host, What: "the Access application \"" + a.Name + "\""}
		}
	}

	include := make([]map[string]any, len(emails))
	for i, e := range emails {
		include[i] = map[string]any{"email": map[string]string{"email": e}}
	}
	policy := map[string]any{"name": name, "decision": "allow", "include": include}
	base := "/accounts/" + url.PathEscape(accountID) + "/access"
	pol, err := c.accessPolicy(ctx, accountID, name)
	if err != nil {
		return "", err
	}
	var made AccessPolicy
	if pol == nil {
		if _, err := c.do(ctx, "create the Access policy", PermAccess, http.MethodPost, base+"/policies", nil, policy, &made); err != nil {
			return "", err
		}
	} else {
		if _, err := c.do(ctx, "update the Access policy", PermAccess, http.MethodPut, base+"/policies/"+url.PathEscape(pol.ID), nil, policy, &made); err != nil {
			return "", err
		}
		if made.ID == "" {
			made.ID = pol.ID
		}
	}
	if made.ID == "" {
		return "", errors.New("cloudflare: the Access policy has no ID")
	}

	app := map[string]any{
		"name": name, "domain": host, "type": "self_hosted", "session_duration": "24h",
		"policies": []map[string]any{{"id": made.ID, "precedence": 1}},
	}
	mine, err := c.accessApps(ctx, accountID, url.Values{"name": {name}, "exact": {"true"}})
	if err != nil {
		return "", err
	}
	var got AccessApp
	for _, a := range mine {
		if a.Name == name {
			if _, err := c.do(ctx, "update the Access application", PermAccess, http.MethodPut, base+"/apps/"+url.PathEscape(a.ID), nil, app, &got); err != nil {
				return "", err
			}
			if got.ID == "" {
				got.ID = a.ID
			}
			return got.ID, nil
		}
	}
	if _, err := c.do(ctx, "create the Access application", PermAccess, http.MethodPost, base+"/apps", nil, app, &got); err != nil {
		return "", err
	}
	return got.ID, nil
}

// removeAccess deletes the Access application and policy named name, if
// there are any. Without the Access permission, there can't be any
// Colaunch made: that isn't an error.
//
// DELETE /accounts/{account_id}/access/apps/{app_id} and
// …/access/policies/{policy_id} (API reference: Delete an Access
// application, Delete an Access reusable policy).
func (c *Client) removeAccess(ctx context.Context, accountID, name string) error {
	base := "/accounts/" + url.PathEscape(accountID) + "/access"
	apps, err := c.accessApps(ctx, accountID, url.Values{"name": {name}, "exact": {"true"}})
	if errors.Is(err, ErrPermission) || errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Name != name {
			continue
		}
		if _, err := c.do(ctx, "delete the Access application", PermAccess, http.MethodDelete, base+"/apps/"+url.PathEscape(a.ID), nil, nil, nil); err != nil {
			return err
		}
	}
	pol, err := c.accessPolicy(ctx, accountID, name)
	if errors.Is(err, ErrPermission) || errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || pol == nil {
		return err
	}
	_, err = c.do(ctx, "delete the Access policy", PermAccess, http.MethodDelete, base+"/policies/"+url.PathEscape(pol.ID), nil, nil, nil)
	return err
}
