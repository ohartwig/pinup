// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Rotation is what `pinup token rotate` does: renew the bot's own token
// before it expires and store the new value where the next run reads it.
//
// Why it exists: on 2026-09-07 the Renovate bot's token expired and every
// hourly run for nineteen hours failed at authentication, across every
// group. The token had carried the self_rotate scope all along; nothing
// called it. A scope without a caller is an intention without a mechanism.
//
// The dangerous moment is between the rotation and the write: GitLab
// revokes the old token as the new one is issued, and a run that dies in
// between leaves the new value nowhere and the old one dead - exactly the
// lockout this exists to prevent. So the write is rehearsed before the
// rotation (the variable is rewritten with the value it already has, which
// proves the role and the scope), retried after it, and the failure case
// shouts. The token value is never written anywhere but the variable.
type Rotation struct {
	Platform *Platform
	// Scope owns the variables: "groups/1210" or "projects/826".
	Scope string
	// Variables are the CI/CD variables that carry the token; every one
	// must hold the value this run authenticates with, or rotating would
	// strand whatever else reads it.
	Variables []string
	// Threshold is how close to expiry the token may get before it is
	// rotated; Lifetime is how long the new one lives.
	Threshold time.Duration
	Lifetime  time.Duration
	// DryRun rehearses the write whatever the expiry and stops before the
	// rotation: the way to prove, today, that the rotation a year out will
	// be allowed to store its result.
	DryRun bool

	Out   io.Writer
	Sleep func(time.Duration)
	// Retries is how often the write after the rotation is attempted.
	Retries int
}

// ErrNoExpiry is returned for a token without an expiry date: nothing can
// be said about how close it is to expiring, and guessing is worse than
// failing, since a failed job is read and a warned one is not.
var ErrNoExpiry = errors.New("the token carries no expiry date; a rotation cannot tell how urgent it is and refuses to guess")

// ErrStranded means the old token is revoked and the new value could not
// be stored: a human has to create a token by hand now.
var ErrStranded = errors.New("the token was rotated but the new value could not be stored: the old token is revoked, the new one is lost - create a token by hand and set the variables")

// Run performs the rotation at now. It returns nil when nothing needed
// doing or the rotation succeeded end to end.
func (r Rotation) Run(ctx context.Context, now time.Time) error {
	out := r.Out
	if out == nil {
		out = io.Discard
	}
	if r.Platform == nil {
		return errors.New("rotation: no platform")
	}
	if len(r.Variables) == 0 || r.Scope == "" {
		return errors.New("rotation: no variables to store the token in")
	}
	self, err := r.Platform.SelfToken(ctx)
	if err != nil {
		return fmt.Errorf("rotation: %w", err)
	}
	if !self.HasExpiry {
		return fmt.Errorf("rotation: token %q (id %d): %w", self.Name, self.ID, ErrNoExpiry)
	}
	if !self.Active || self.Revoked {
		return fmt.Errorf("rotation: token %q (id %d) is not active; a human has to create a new one", self.Name, self.ID)
	}
	left := self.ExpiresAt.Sub(now.UTC().Truncate(24 * time.Hour))
	fmt.Fprintf(out, "token %q (id %d) expires %s, %s left, threshold %s\n", self.Name, self.ID, self.ExpiresAt.Format("2006-01-02"), days(left), days(r.Threshold))
	if left > r.Threshold && !r.DryRun {
		fmt.Fprintln(out, "nothing to do")
		return nil
	}

	// Every variable must carry this very token, and the write must be
	// possible - both checked, in that order, before anything
	// irreversible happens.
	for _, key := range r.Variables {
		cur, err := r.Platform.Variable(ctx, r.Scope, key)
		if err != nil {
			return fmt.Errorf("rotation: before rotating: %w", err)
		}
		if cur != r.Platform.token.Value {
			return fmt.Errorf("rotation: %s/%s does not hold the token this run authenticates with; rotating would strand whatever reads it", r.Scope, key)
		}
	}
	for _, key := range r.Variables {
		if err := r.Platform.SetVariable(ctx, r.Scope, key, r.Platform.token.Value); err != nil {
			return fmt.Errorf("rotation: rehearsing the write: %w", err)
		}
	}
	fmt.Fprintf(out, "rehearsed writing %s/{%s}: the role and the scope allow it\n", r.Scope, strings.Join(r.Variables, ","))
	if r.DryRun {
		fmt.Fprintf(out, "dry run: would rotate for %s and store the new value\n", days(r.Lifetime))
		return nil
	}

	value, err := r.Platform.RotateSelf(ctx, now.Add(r.Lifetime))
	if err != nil {
		return fmt.Errorf("rotation: %w", err)
	}
	fmt.Fprintln(out, "rotated; the old token is revoked from here on")
	// From here on only the new token is accepted - the store and the
	// check both speak with it.
	fresh := r.Platform.WithToken(value)

	retries := r.Retries
	if retries <= 0 {
		retries = 5
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for _, key := range r.Variables {
		var last error
		for attempt := 0; attempt < retries; attempt++ {
			if attempt > 0 {
				sleep(time.Duration(1<<uint(attempt-1)) * time.Second)
			}
			last = fresh.SetVariable(ctx, r.Scope, key, value)
			if last == nil {
				break
			}
			fmt.Fprintf(out, "storing %s/%s failed (attempt %d of %d): %v\n", r.Scope, key, attempt+1, retries, last)
		}
		if last != nil {
			return fmt.Errorf("rotation: %s/%s: %w: %v", r.Scope, key, ErrStranded, last)
		}
		fmt.Fprintf(out, "stored %s/%s\n", r.Scope, key)
	}

	check, err := fresh.SelfToken(ctx)
	if err != nil {
		return fmt.Errorf("rotation: the new token does not answer for itself: %w", err)
	}
	fmt.Fprintf(out, "the new token (id %d) is active and expires %s\n", check.ID, check.ExpiresAt.Format("2006-01-02"))
	return nil
}

func days(d time.Duration) string {
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}
