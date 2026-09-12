// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// SelfToken is what GitLab says about the token a Platform authenticates
// with: /personal_access_tokens/self. ExpiresAt is zero and HasExpiry false
// for a token without an expiry date - which GitLab no longer issues, but
// an old one may still exist, and a rotation that cannot read the date it
// is meant to beat must not guess.
type SelfToken struct {
	ID        int
	Name      string
	Scopes    []string
	Active    bool
	Revoked   bool
	ExpiresAt time.Time
	HasExpiry bool
}

// SelfToken describes the token this Platform carries.
func (p *Platform) SelfToken(ctx context.Context) (SelfToken, error) {
	resp, err := p.do(ctx, http.MethodGet, p.base+"/api/v4/personal_access_tokens/self", nil)
	if err != nil {
		return SelfToken{}, err
	}
	if err := classifyToken(resp, "read the token itself"); err != nil {
		return SelfToken{}, err
	}
	var payload struct {
		ID        int      `json:"id"`
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		Active    bool     `json:"active"`
		Revoked   bool     `json:"revoked"`
		ExpiresAt string   `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return SelfToken{}, fmt.Errorf("gitlab: decode token: %w", err)
	}
	t := SelfToken{ID: payload.ID, Name: payload.Name, Scopes: payload.Scopes, Active: payload.Active, Revoked: payload.Revoked}
	if payload.ExpiresAt != "" {
		exp, err := time.Parse("2006-01-02", payload.ExpiresAt)
		if err != nil {
			return SelfToken{}, fmt.Errorf("gitlab: token expiry %q is not a date: %w", payload.ExpiresAt, err)
		}
		t.ExpiresAt, t.HasExpiry = exp, true
	}
	return t, nil
}

// RotateSelf rotates the token this Platform carries - GitLab revokes the
// old one the moment the new one exists - and returns the new value. The
// value is returned and nothing else: it must reach the place the next run
// reads it from, and it must never reach a log.
func (p *Platform) RotateSelf(ctx context.Context, expiresAt time.Time) (string, error) {
	q := url.Values{"expires_at": {expiresAt.UTC().Format("2006-01-02")}}
	resp, err := p.do(ctx, http.MethodPost, p.base+"/api/v4/personal_access_tokens/self/rotate?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if err := classifyToken(resp, "rotate the token"); err != nil {
		return "", err
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return "", fmt.Errorf("gitlab: decode rotated token: %w", err)
	}
	if payload.Token == "" {
		return "", fmt.Errorf("gitlab: the rotation answered without a token value")
	}
	return payload.Token, nil
}

// Variable reads one CI/CD variable of a scope - "groups/1210" or
// "projects/826" - and returns its value.
func (p *Platform) Variable(ctx context.Context, scope, key string) (string, error) {
	resp, err := p.do(ctx, http.MethodGet, p.variableURL(scope, key), nil)
	if err != nil {
		return "", err
	}
	if err := classifyToken(resp, "read variable "+key+" of "+scope); err != nil {
		return "", err
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(resp.body, &payload); err != nil {
		return "", fmt.Errorf("gitlab: decode variable %s: %w", key, err)
	}
	return payload.Value, nil
}

// SetVariable writes one CI/CD variable's value; every other attribute
// (masked, protected, environment scope) stays as it is.
func (p *Platform) SetVariable(ctx context.Context, scope, key, value string) error {
	resp, err := p.do(ctx, http.MethodPut, p.variableURL(scope, key), map[string]any{"value": value})
	if err != nil {
		return err
	}
	return classifyToken(resp, "write variable "+key+" of "+scope)
}

// WithToken returns a Platform on the same instance and transport that
// authenticates with another personal access token.
func (p *Platform) WithToken(value string) *Platform {
	return &Platform{hc: p.hc, base: p.base, token: Token{Value: value, Header: "PRIVATE-TOKEN"}}
}

func (p *Platform) variableURL(scope, key string) string {
	return fmt.Sprintf("%s/api/v4/%s/variables/%s", p.base, scope, url.PathEscape(key))
}

// classifyToken is classify for the token and variable endpoints, which
// have no project to name; what is named is the action that was refused.
func classifyToken(resp *apiResponse, action string) error {
	switch {
	case resp.status >= 200 && resp.status < 300:
		return nil
	case resp.status == http.StatusUnauthorized:
		return fmt.Errorf("gitlab: cannot %s: the token is not accepted (401) - expired or revoked", action)
	case resp.status == http.StatusForbidden:
		return fmt.Errorf("gitlab: cannot %s: forbidden (403) - the token's scopes or its user's role do not allow it", action)
	case resp.status == http.StatusNotFound:
		return fmt.Errorf("gitlab: cannot %s: not found (404)", action)
	default:
		if msg := apiMessage(resp.body); msg != "" {
			return fmt.Errorf("gitlab: cannot %s: status %d: %s", action, resp.status, msg)
		}
		return fmt.Errorf("gitlab: cannot %s: unexpected status %d", action, resp.status)
	}
}
