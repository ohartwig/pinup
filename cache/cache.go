// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package cache implements a single-file embedded store, backed by
// go.etcd.io/bbolt, that pinup mounts as a CI cache volume.
//
// It holds TTL'd datasource lookup results ("releases") and, most
// importantly, the first time pinup ever observed a given (source, version)
// pair ("firstseen"). Some datasources publish no release timestamp at all;
// for those, the age of a release is defined entirely by when pinup first
// saw it. That makes firstseen correctness-relevant rather than a cache
// speed-up: losing it, even partially, silently reflows every "how old is
// this" decision the estate makes.
//
// Every exported method that needs the current time takes it as a
// parameter. This package must never call time.Now() itself, so that
// callers (and tests) fully control what "now" means.
package cache

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.etcd.io/bbolt"
)

// currentSchemaVersion identifies the on-disk shape of the releases and
// firstseen buckets. Bump it whenever that shape changes incompatibly.
const currentSchemaVersion uint32 = 1

// Bucket and key names. Kept as package vars (bbolt wants []byte, and a
// var avoids repeated []byte(...) conversions of a string literal).
var (
	schemaBucket     = []byte("_schema")
	releasesBucket   = []byte("releases")
	firstSeenBucket  = []byte("firstseen")
	schemaVersionKey = []byte("version")
)

// errBucketMissing signals a store whose required buckets are absent even
// though ensureSchema is supposed to have created them at Open. It should
// be unreachable in practice; it exists so a corrupted file fails loudly
// instead of nil-pointer-panicking deep inside bbolt.
var errBucketMissing = errors.New("cache: required bucket missing (corrupt store?)")

// Store is a handle to a single bbolt-backed cache file.
type Store struct {
	db *bbolt.DB
}

// Stats summarizes the current contents of a Store.
type Stats struct {
	Releases      int
	FirstSeen     int
	SchemaVersion uint32
}

// Open opens (creating if necessary) the cache file at path.
//
// If the file already contains a schema version different from
// currentSchemaVersion, Open invalidates the releases and firstseen buckets
// rather than reading them with the wrong shape: a stale schema misread
// silently produces wrong answers, while an invalidated store just starts
// cold.
func Open(path string) (*Store, error) {
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("cache: open %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cache: open %s: %w", path, err)
	}
	return s, nil
}

// ensureSchema creates the working buckets on a fresh file, or wipes and
// recreates them if the stored schema version doesn't match ours.
func (s *Store) ensureSchema() error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		schema, err := tx.CreateBucketIfNotExists(schemaBucket)
		if err != nil {
			return err
		}

		raw := schema.Get(schemaVersionKey)
		if raw != nil && (len(raw) != 4 || binary.BigEndian.Uint32(raw) != currentSchemaVersion) {
			// Wrong or corrupted shape (tampered, truncated, or written by an
			// older/newer pinup). Reading it as the current format could
			// silently misinterpret bytes, e.g. mistaking a release payload
			// for a firstseen timestamp. Drop the old data and start cold
			// instead: a clean invalidation is far cheaper than a plan built
			// on garbage.
			if err := dropBucketIfExists(tx, releasesBucket); err != nil {
				return err
			}
			if err := dropBucketIfExists(tx, firstSeenBucket); err != nil {
				return err
			}
		}

		if err := writeSchemaVersion(schema); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(releasesBucket); err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists(firstSeenBucket)
		return err
	})
}

func dropBucketIfExists(tx *bbolt.Tx, name []byte) error {
	if err := tx.DeleteBucket(name); err != nil && !errors.Is(err, bbolt.ErrBucketNotFound) {
		return err
	}
	return nil
}

func writeSchemaVersion(schema *bbolt.Bucket) error {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, currentSchemaVersion)
	return schema.Put(schemaVersionKey, buf)
}

// Close releases the underlying file handle and lock.
func (s *Store) Close() error {
	return s.db.Close()
}

// PutReleases stores payload under key, stamped with now so a later
// GetReleases can judge freshness against a caller-supplied TTL.
func (s *Store) PutReleases(key string, payload []byte, now time.Time) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(releasesBucket)
		if b == nil {
			return errBucketMissing
		}
		return b.Put([]byte(key), encodeRelease(now, payload))
	})
}

// GetReleases returns the payload last stored under key, if any, along with
// whether it is still fresh under ttl as of now. A missing key is reported
// as (nil, false, nil): a plain cache miss, not an error.
//
// A stale entry is still returned (fresh=false): this store is meant for
// stale-while-revalidate use, where callers prefer an old answer to no
// answer while a refresh is in flight.
func (s *Store) GetReleases(key string, ttl time.Duration, now time.Time) ([]byte, bool, error) {
	var payload []byte
	var fresh bool
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(releasesBucket)
		if b == nil {
			return errBucketMissing
		}
		raw := b.Get([]byte(key))
		if raw == nil {
			return nil
		}
		storedAt, p, ok := decodeRelease(raw)
		if !ok {
			// Corrupt record: treat like a miss rather than surfacing an
			// error for what is, from the caller's point of view, cache
			// content that can simply be refetched.
			return nil
		}
		payload = p
		fresh = !now.After(storedAt.Add(ttl))
		return nil
	})
	return payload, fresh, err
}

// encodeRelease packs storedAt and payload into a single bbolt value: an
// 8-byte big-endian UnixNano timestamp followed by the raw payload bytes.
// A hand-rolled fixed layout keeps the hot Put/Get path allocation-light
// and avoids pulling a serialization format into what is, structurally, a
// timestamp plus an opaque blob.
func encodeRelease(storedAt time.Time, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint64(buf[:8], uint64(storedAt.UnixNano()))
	copy(buf[8:], payload)
	return buf
}

// decodeRelease reverses encodeRelease. The returned payload is a copy:
// bbolt's Get gives back memory that is only valid inside the enclosing
// transaction, so it must never be returned to the caller as-is.
func decodeRelease(raw []byte) (storedAt time.Time, payload []byte, ok bool) {
	if len(raw) < 8 {
		return time.Time{}, nil, false
	}
	nanos := int64(binary.BigEndian.Uint64(raw[:8]))
	payload = append([]byte(nil), raw[8:]...)
	return time.Unix(0, nanos).UTC(), payload, true
}

// FirstSeen records the moment (key, version) was first observed and
// returns that recorded moment on every call, including the first. It is
// not a lookup that can miss: the first call establishes the answer for
// all future ones, and later calls with a different now do not move it.
//
// firstseen entries live in nested buckets, one sub-bucket per key holding
// version -> encoded time.Time, so unrelated keys and versions can never
// collide the way a delimited composite string key could.
func (s *Store) FirstSeen(key, version string, now time.Time) (time.Time, error) {
	var result time.Time
	err := s.db.Update(func(tx *bbolt.Tx) error {
		top := tx.Bucket(firstSeenBucket)
		if top == nil {
			return errBucketMissing
		}
		sub, err := top.CreateBucketIfNotExists([]byte(key))
		if err != nil {
			return err
		}

		if raw := sub.Get([]byte(version)); raw != nil {
			t, err := decodeTime(raw)
			if err != nil {
				return err
			}
			result = t
			return nil
		}

		// First observation of this pair: the answer is permanent from here
		// on, so it is written before anything derived from it is returned.
		result = now.UTC()
		encoded, err := result.MarshalBinary()
		if err != nil {
			return err
		}
		return sub.Put([]byte(version), encoded)
	})
	return result, err
}

// FirstSeenCount returns the total number of (key, version) pairs recorded
// across all firstseen entries.
func (s *Store) FirstSeenCount() (int, error) {
	count := 0
	err := s.db.View(func(tx *bbolt.Tx) error {
		top := tx.Bucket(firstSeenBucket)
		if top == nil {
			return errBucketMissing
		}
		n, err := countFirstSeen(top)
		count = n
		return err
	})
	return count, err
}

func countFirstSeen(top *bbolt.Bucket) (int, error) {
	count := 0
	err := top.ForEach(func(k, v []byte) error {
		if v != nil {
			// Not expected: firstseen should only ever hold nested
			// per-key buckets, never a leaf value directly under top.
			return nil
		}
		sub := top.Bucket(k)
		return sub.ForEach(func(_, _ []byte) error {
			count++
			return nil
		})
	})
	return count, err
}

func decodeTime(raw []byte) (time.Time, error) {
	var t time.Time
	if err := t.UnmarshalBinary(raw); err != nil {
		return time.Time{}, fmt.Errorf("cache: decoding firstseen timestamp: %w", err)
	}
	return t, nil
}

// Prune deletes release entries stored more than olderThan before now. It
// never touches the firstseen bucket: firstseen records are permanent by
// definition, and pruning them would make every affected version look
// brand-new the next time it's evaluated.
func (s *Store) Prune(olderThan time.Duration, now time.Time) (int, error) {
	cutoff := now.Add(-olderThan)
	removed := 0
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(releasesBucket)
		if b == nil {
			return errBucketMissing
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			storedAt, _, ok := decodeRelease(v)
			if !ok || storedAt.Before(cutoff) {
				if err := c.Delete(); err != nil {
					return err
				}
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// Export writes a complete, consistent snapshot of the store (all buckets,
// including firstseen) to w. It is the counterpart to Import: the intended
// disaster recovery path for a lost cache volume is republishing this
// snapshot as a build artefact and Import-ing it into a fresh store.
func (s *Store) Export(w io.Writer) error {
	return s.db.View(func(tx *bbolt.Tx) error {
		_, err := tx.WriteTo(w)
		return err
	})
}

// Import merges a snapshot produced by Export into this store. Keys present
// in the snapshot overwrite any existing value for the same key; keys not
// mentioned in the snapshot are left untouched.
//
// bbolt has no API for reading a foreign snapshot's buckets without first
// opening it as a database file, so the snapshot is staged to a temporary
// file, opened read-only, and copied bucket by bucket (recursing into the
// nested per-key firstseen buckets).
func (s *Store) Import(r io.Reader) error {
	tmp, err := os.CreateTemp("", "pinup-cache-import-*.db")
	if err != nil {
		return fmt.Errorf("cache: import: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cache: import: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: import: %w", err)
	}

	src, err := bbolt.Open(tmpPath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("cache: import: opening snapshot: %w", err)
	}
	defer src.Close()

	return s.db.Update(func(dstTx *bbolt.Tx) error {
		return src.View(func(srcTx *bbolt.Tx) error {
			for _, name := range [][]byte{schemaBucket, releasesBucket, firstSeenBucket} {
				if err := importBucket(dstTx, srcTx, name); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func importBucket(dstTx, srcTx *bbolt.Tx, name []byte) error {
	srcBucket := srcTx.Bucket(name)
	if srcBucket == nil {
		return nil
	}
	dstBucket, err := dstTx.CreateBucketIfNotExists(name)
	if err != nil {
		return err
	}
	return copyBucketContents(srcBucket, dstBucket)
}

func copyBucketContents(src, dst *bbolt.Bucket) error {
	return src.ForEach(func(k, v []byte) error {
		if v == nil {
			// Nested bucket: firstseen's per-key sub-buckets.
			srcSub := src.Bucket(k)
			dstSub, err := dst.CreateBucketIfNotExists(k)
			if err != nil {
				return err
			}
			return copyBucketContents(srcSub, dstSub)
		}
		return dst.Put(k, v)
	})
}

// Stats reports the current size of the store.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.View(func(tx *bbolt.Tx) error {
		releases := tx.Bucket(releasesBucket)
		if releases == nil {
			return errBucketMissing
		}
		if err := releases.ForEach(func(_, _ []byte) error {
			st.Releases++
			return nil
		}); err != nil {
			return err
		}

		schema := tx.Bucket(schemaBucket)
		if schema == nil {
			return errBucketMissing
		}
		if raw := schema.Get(schemaVersionKey); len(raw) == 4 {
			st.SchemaVersion = binary.BigEndian.Uint32(raw)
		}

		top := tx.Bucket(firstSeenBucket)
		if top == nil {
			return errBucketMissing
		}
		n, err := countFirstSeen(top)
		st.FirstSeen = n
		return err
	})
	return st, err
}
