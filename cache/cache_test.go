// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package cache

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

// openTestStore opens a Store backed by a fresh file under t.TempDir() and
// arranges for it to be closed at the end of the test.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestGetReleasesFreshness(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		age       time.Duration
		ttl       time.Duration
		wantFresh bool
	}{
		{"well within ttl", 5 * time.Minute, 10 * time.Minute, true},
		{"exactly at ttl boundary", 10 * time.Minute, 10 * time.Minute, true},
		{"past ttl", 11 * time.Minute, 10 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			if err := store.PutReleases("k", []byte("payload"), base); err != nil {
				t.Fatalf("PutReleases: %v", err)
			}

			payload, fresh, err := store.GetReleases("k", tc.ttl, base.Add(tc.age))
			if err != nil {
				t.Fatalf("GetReleases: %v", err)
			}
			// Stale-while-revalidate: the payload must come back even when
			// fresh is false, so a caller can serve it while refreshing.
			if string(payload) != "payload" {
				t.Errorf("payload = %q, want %q", payload, "payload")
			}
			if fresh != tc.wantFresh {
				t.Errorf("fresh = %v, want %v", fresh, tc.wantFresh)
			}
		})
	}
}

func TestGetReleasesMissIsNotAnError(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	payload, fresh, err := store.GetReleases("nonexistent", time.Hour, now)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %q, want nil", payload)
	}
	if fresh {
		t.Errorf("fresh = true, want false for a cache miss")
	}
}

func TestFirstSeenIsStableAcrossCalls(t *testing.T) {
	store := openTestStore(t)
	first := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	got, err := store.FirstSeen("pkg", "1.2.3", first)
	if err != nil {
		t.Fatalf("FirstSeen (first call): %v", err)
	}
	if !got.Equal(first) {
		t.Errorf("first call = %v, want %v", got, first)
	}

	// A later call with a much later "now" must not move the recorded time.
	later := first.Add(72 * time.Hour)
	got2, err := store.FirstSeen("pkg", "1.2.3", later)
	if err != nil {
		t.Fatalf("FirstSeen (second call): %v", err)
	}
	if !got2.Equal(first) {
		t.Errorf("second call = %v, want unchanged %v", got2, first)
	}
}

func TestPruneRemovesStaleReleasesButKeepsFirstSeen(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, key := range []string{"a", "b", "c"} {
		if err := store.PutReleases(key, []byte(key), base); err != nil {
			t.Fatalf("PutReleases(%s): %v", key, err)
		}
		if _, err := store.FirstSeen(key, "1.0.0", base); err != nil {
			t.Fatalf("FirstSeen(%s): %v", key, err)
		}
	}

	before, err := store.FirstSeenCount()
	if err != nil {
		t.Fatalf("FirstSeenCount (before): %v", err)
	}
	if before != 3 {
		t.Fatalf("FirstSeenCount (before) = %d, want 3", before)
	}

	// Prune aggressively: a 1ns TTL evaluated a year later removes every
	// release entry ever written, however recent.
	muchLater := base.Add(365 * 24 * time.Hour)
	removed, err := store.Prune(time.Nanosecond, muchLater)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 3 {
		t.Errorf("removed = %d, want 3", removed)
	}

	if _, fresh, err := store.GetReleases("a", time.Hour, muchLater); err != nil {
		t.Fatalf("GetReleases after prune: %v", err)
	} else if fresh {
		t.Errorf("GetReleases(a) fresh = true after prune, want gone/stale")
	}

	after, err := store.FirstSeenCount()
	if err != nil {
		t.Fatalf("FirstSeenCount (after): %v", err)
	}
	if after != before {
		t.Errorf("FirstSeenCount (after) = %d, want unchanged %d (Prune must never touch firstseen)", after, before)
	}

	// Not just the count: the actual recorded times must be untouched too.
	for _, key := range []string{"a", "b", "c"} {
		got, err := store.FirstSeen(key, "1.0.0", muchLater)
		if err != nil {
			t.Fatalf("FirstSeen(%s) after prune: %v", key, err)
		}
		if !got.Equal(base) {
			t.Errorf("FirstSeen(%s) after prune = %v, want %v", key, got, base)
		}
	}
}

func TestExportImportRoundTripsFirstSeen(t *testing.T) {
	src := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	versions := map[string]string{"alpha": "1.0.0", "beta": "2.3.4"}
	for key, version := range versions {
		if _, err := src.FirstSeen(key, version, base); err != nil {
			t.Fatalf("FirstSeen(%s): %v", key, err)
		}
	}
	if err := src.PutReleases("alpha", []byte("payload"), base); err != nil {
		t.Fatalf("PutReleases: %v", err)
	}

	var buf bytes.Buffer
	if err := src.Export(&buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := openTestStore(t)
	if err := dst.Import(&buf); err != nil {
		t.Fatalf("Import: %v", err)
	}

	gotCount, err := dst.FirstSeenCount()
	if err != nil {
		t.Fatalf("FirstSeenCount: %v", err)
	}
	if gotCount != len(versions) {
		t.Errorf("FirstSeenCount = %d, want %d", gotCount, len(versions))
	}

	for key, version := range versions {
		// A later "now" here must not shift the imported time: Import
		// restores an established fact, it doesn't reset the clock.
		got, err := dst.FirstSeen(key, version, base.Add(time.Hour))
		if err != nil {
			t.Fatalf("FirstSeen(%s) after import: %v", key, err)
		}
		if !got.Equal(base) {
			t.Errorf("FirstSeen(%s) after import = %v, want %v", key, got, base)
		}
	}

	payload, fresh, err := dst.GetReleases("alpha", time.Hour, base)
	if err != nil {
		t.Fatalf("GetReleases after import: %v", err)
	}
	if string(payload) != "payload" || !fresh {
		t.Errorf("GetReleases after import = (%q, %v), want (%q, true)", payload, fresh, "payload")
	}
}

// tamperSchemaVersion opens path directly with bbolt (bypassing this
// package's Open) and overwrites the _schema bucket's version key, to
// simulate an old or corrupted on-disk format.
func tamperSchemaVersion(t *testing.T, path string, version uint32) {
	t.Helper()
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("tamperSchemaVersion: open: %v", err)
	}
	defer db.Close()

	err = db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(schemaBucket)
		if err != nil {
			return err
		}
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, version)
		return b.Put(schemaVersionKey, buf)
	})
	if err != nil {
		t.Fatalf("tamperSchemaVersion: update: %v", err)
	}
}

func TestTamperedSchemaVersionInvalidatesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.FirstSeen("pkg", "1.0.0", base); err != nil {
		t.Fatalf("FirstSeen: %v", err)
	}
	if err := store.PutReleases("pkg", []byte("payload"), base); err != nil {
		t.Fatalf("PutReleases: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// currentSchemaVersion is 1; write a version that can never match it.
	tamperSchemaVersion(t, path, currentSchemaVersion+1)

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open (reopened): %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	// A schema mismatch must invalidate cleanly, not silently misread old
	// bytes as the current shape: both buckets should come back empty.
	count, err := reopened.FirstSeenCount()
	if err != nil {
		t.Fatalf("FirstSeenCount: %v", err)
	}
	if count != 0 {
		t.Errorf("FirstSeenCount after schema mismatch = %d, want 0", count)
	}

	_, fresh, err := reopened.GetReleases("pkg", time.Hour, base)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if fresh {
		t.Errorf("GetReleases fresh = true after schema mismatch, want gone")
	}

	st, err := reopened.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.SchemaVersion != currentSchemaVersion {
		t.Errorf("Stats.SchemaVersion = %d, want %d (must be rewritten to current)", st.SchemaVersion, currentSchemaVersion)
	}
}

func TestReopenPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.PutReleases("k", []byte("v1"), base); err != nil {
		t.Fatalf("PutReleases: %v", err)
	}
	if _, err := store.FirstSeen("k", "1.0.0", base); err != nil {
		t.Fatalf("FirstSeen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open (reopened): %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	payload, fresh, err := reopened.GetReleases("k", time.Hour, base)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if string(payload) != "v1" || !fresh {
		t.Errorf("GetReleases after reopen = (%q, %v), want (%q, true)", payload, fresh, "v1")
	}

	got, err := reopened.FirstSeen("k", "1.0.0", base.Add(time.Hour))
	if err != nil {
		t.Fatalf("FirstSeen after reopen: %v", err)
	}
	if !got.Equal(base) {
		t.Errorf("FirstSeen after reopen = %v, want %v", got, base)
	}
}

func TestStatsReportsCounts(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := store.PutReleases("a", []byte("x"), base); err != nil {
		t.Fatalf("PutReleases: %v", err)
	}
	if err := store.PutReleases("b", []byte("y"), base); err != nil {
		t.Fatalf("PutReleases: %v", err)
	}
	if _, err := store.FirstSeen("a", "1.0.0", base); err != nil {
		t.Fatalf("FirstSeen: %v", err)
	}

	st, err := store.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Releases != 2 {
		t.Errorf("Stats.Releases = %d, want 2", st.Releases)
	}
	if st.FirstSeen != 1 {
		t.Errorf("Stats.FirstSeen = %d, want 1", st.FirstSeen)
	}
	if st.SchemaVersion != currentSchemaVersion {
		t.Errorf("Stats.SchemaVersion = %d, want %d", st.SchemaVersion, currentSchemaVersion)
	}
}

// FirstSeenAll answers exactly as FirstSeen would, per version, and a
// second call with a later now moves nothing.
func TestFirstSeenAllMatchesFirstSeen(t *testing.T) {
	s := openTestStore(t)
	t0 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	single, err := s.FirstSeen("k", "1.0.0", t0)
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.FirstSeenAll("k", []string{"1.0.0", "1.1.0"}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !all["1.0.0"].Equal(single) {
		t.Errorf("1.0.0: batch %v, single %v", all["1.0.0"], single)
	}
	if !all["1.1.0"].Equal(t0.Add(time.Hour)) {
		t.Errorf("1.1.0 first seen at %v", all["1.1.0"])
	}
	again, err := s.FirstSeenAll("k", []string{"1.1.0"}, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !again["1.1.0"].Equal(t0.Add(time.Hour)) {
		t.Errorf("a later call moved the first-seen moment to %v", again["1.1.0"])
	}
	if n, _ := s.FirstSeenCount(); n != 2 {
		t.Errorf("count = %d", n)
	}
}
