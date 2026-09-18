// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

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
	// Targets are further places the token is stored, each with a scope
	// of its own: the token that serves two projects lives in a variable
	// of each, and both are rewritten. Scope and Variables are the
	// shorthand for targets under one scope.
	Targets []Target
	// Others are tokens beside the one this run authenticates with, kept
	// by the same account - a read-only token the dry-run partitions
	// carry, say. Each is rotated by id with this run's token, which
	// therefore needs the api scope, and stored in its own targets. They
	// rotate before the run's own token does: that one's rotation ends
	// the credential everything else here speaks with.
	Others []Other
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

// Target is one CI/CD variable a token is stored in.
type Target struct {
	Scope string // "groups/1210" or "projects/826"
	Key   string
}

func (t Target) String() string { return t.Scope + "/" + t.Key }

// Other is a further token of the same account, rotated alongside.
type Other struct {
	// Name says which token this is in messages; Value is the token.
	Name    string
	Value   string
	Targets []Target
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
	targets := append([]Target(nil), r.Targets...)
	for _, key := range r.Variables {
		targets = append(targets, Target{Scope: r.Scope, Key: key})
	}
	if len(targets) == 0 {
		return errors.New("rotation: no variables to store the token in")
	}
	for _, t := range targets {
		if t.Scope == "" || t.Key == "" {
			return fmt.Errorf("rotation: a target needs a scope and a key, got %q", t)
		}
	}
	self, err := r.Platform.SelfToken(ctx)
	if err != nil {
		return fmt.Errorf("rotation: %w", err)
	}
	dueSelf, err := r.due(out, "the run's own token", self, now)
	if err != nil {
		return err
	}
	// The other tokens: each answers for itself, each is due on its own
	// expiry, and each is checked and rehearsed like the run's own.
	type pending struct {
		other Other
		info  SelfToken
		due   bool
	}
	var others []pending
	for _, o := range r.Others {
		if o.Value == "" || len(o.Targets) == 0 {
			return fmt.Errorf("rotation: token %q needs a value and targets", o.Name)
		}
		info, err := r.Platform.WithToken(o.Value).SelfToken(ctx)
		if err != nil {
			return fmt.Errorf("rotation: token %q: %w", o.Name, err)
		}
		due, err := r.due(out, "token "+o.Name, info, now)
		if err != nil {
			return err
		}
		others = append(others, pending{o, info, due})
	}
	anyDue := dueSelf
	for _, p := range others {
		anyDue = anyDue || p.due
	}
	if !anyDue && !r.DryRun {
		fmt.Fprintln(out, "nothing to do")
		return nil
	}

	// Every variable must carry its token, and the write must be
	// possible - both checked, in that order, before anything
	// irreversible happens. The writes are this run's token's, whatever
	// token the variable holds: only it has the role to write.
	if err := r.rehearse(ctx, out, targets, r.Platform.token.Value, "the token this run authenticates with"); err != nil {
		return err
	}
	for _, p := range others {
		if err := r.rehearse(ctx, out, p.other.Targets, p.other.Value, "token "+p.other.Name); err != nil {
			return err
		}
	}
	if r.DryRun {
		fmt.Fprintf(out, "dry run: would rotate for %s and store the new values\n", days(r.Lifetime))
		return nil
	}

	// The others first: their rotation is done with this run's token,
	// which must still be valid.
	for _, p := range others {
		if !p.due {
			continue
		}
		value, err := r.Platform.RotateByID(ctx, p.info.ID, now.Add(r.Lifetime))
		if err != nil {
			return fmt.Errorf("rotation: token %q: %w", p.other.Name, err)
		}
		fmt.Fprintf(out, "rotated token %q (id %d); the old value is revoked from here on\n", p.other.Name, p.info.ID)
		if err := r.store(ctx, out, r.Platform, p.other.Targets, value); err != nil {
			return err
		}
		if _, err := r.Platform.WithToken(value).SelfToken(ctx); err != nil {
			return fmt.Errorf("rotation: the new token %q does not answer for itself: %w", p.other.Name, err)
		}
	}
	if !dueSelf {
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
	if err := r.store(ctx, out, fresh, targets, value); err != nil {
		return err
	}
	check, err := fresh.SelfToken(ctx)
	if err != nil {
		return fmt.Errorf("rotation: the new token does not answer for itself: %w", err)
	}
	fmt.Fprintf(out, "the new token (id %d) is active and expires %s\n", check.ID, check.ExpiresAt.Format("2006-01-02"))
	return nil
}

// due reports whether a token is close enough to its expiry to rotate,
// refusing a token without one and a token that is not active.
func (r Rotation) due(out io.Writer, what string, t SelfToken, now time.Time) (bool, error) {
	if !t.HasExpiry {
		return false, fmt.Errorf("rotation: %s %q (id %d): %w", what, t.Name, t.ID, ErrNoExpiry)
	}
	if !t.Active || t.Revoked {
		return false, fmt.Errorf("rotation: %s %q (id %d) is not active; a human has to create a new one", what, t.Name, t.ID)
	}
	left := t.ExpiresAt.Sub(now.UTC().Truncate(24 * time.Hour))
	fmt.Fprintf(out, "%s %q (id %d) expires %s, %s left, threshold %s\n", what, t.Name, t.ID, t.ExpiresAt.Format("2006-01-02"), days(left), days(r.Threshold))
	return left <= r.Threshold, nil
}

// rehearse checks that every target holds value and rewrites it with the
// value it has, which proves the role and the scope allow the write.
func (r Rotation) rehearse(ctx context.Context, out io.Writer, targets []Target, value, what string) error {
	for _, t := range targets {
		cur, err := r.Platform.Variable(ctx, t.Scope, t.Key)
		if err != nil {
			return fmt.Errorf("rotation: before rotating: %w", err)
		}
		if cur != value {
			return fmt.Errorf("rotation: %s does not hold %s; rotating would strand whatever reads it", t, what)
		}
	}
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		if err := r.Platform.SetVariable(ctx, t.Scope, t.Key, value); err != nil {
			return fmt.Errorf("rotation: rehearsing the write: %w", err)
		}
		names = append(names, t.String())
	}
	fmt.Fprintf(out, "rehearsed writing %s: the role and the scope allow it\n", strings.Join(names, ", "))
	return nil
}

// store writes value into every target with p, retrying: a failure here
// is the lockout the rehearsal exists to prevent, and it shouts.
func (r Rotation) store(ctx context.Context, out io.Writer, p *Platform, targets []Target, value string) error {
	retries := r.Retries
	if retries <= 0 {
		retries = 5
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for _, t := range targets {
		var last error
		for attempt := 0; attempt < retries; attempt++ {
			if attempt > 0 {
				sleep(time.Duration(1<<uint(attempt-1)) * time.Second)
			}
			last = p.SetVariable(ctx, t.Scope, t.Key, value)
			if last == nil {
				break
			}
			fmt.Fprintf(out, "storing %s failed (attempt %d of %d): %v\n", t, attempt+1, retries, last)
		}
		if last != nil {
			return fmt.Errorf("rotation: %s: %w: %v", t, ErrStranded, last)
		}
		fmt.Fprintf(out, "stored %s\n", t)
	}
	return nil
}

func days(d time.Duration) string {
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}
