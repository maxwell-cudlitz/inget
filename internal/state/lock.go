// Locking: one run per datatype, enforced by the state store.
//
// Kubernetes' CronJob controller may create zero or two jobs for a given tick, so
// concurrencyPolicy: Forbid is necessary but not sufficient. Lock is the other half: a
// second process fails fast with ErrLocked instead of duplicating work and paying twice for
// the same tokens (D13).
//
// The two drivers differ in what happens when the holder dies. A PostgreSQL session
// advisory lock is released by the server when the connection drops, so a killed pod needs
// no cleanup. SQLite has no equivalent, so its lock is a row, and a killed process leaves
// that row behind for `inget state unlock` to remove. That difference is one of the reasons
// postgres is the default and sqlite is for development.
package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrLocked reports that another process holds the lock. Callers branch on it to exit
// cleanly rather than to retry.
var ErrLocked = errors.New("lock is held by another process")

// releaseTimeout bounds a release attempt. Release runs on the way out of a run, often
// because the process was told to stop, so it cannot inherit the run's context.
const releaseTimeout = 10 * time.Second

const (
	advisoryLockSQL   = `SELECT pg_try_advisory_lock(hashtext(?))`
	advisoryUnlockSQL = `SELECT pg_advisory_unlock(hashtext(?))`

	mutexLockSQL   = `INSERT INTO locks (lock_key, holder) VALUES (?, ?) ON CONFLICT (lock_key) DO NOTHING`
	mutexUnlockSQL = `DELETE FROM locks WHERE lock_key = ? AND holder = ?`
)

// Lock implements Store.
func (s *store) Lock(ctx context.Context, key string) (func() error, error) {
	if key == "" {
		return nil, errors.New("lock key is required")
	}
	if s.d.advisory {
		return s.advisoryLock(ctx, key)
	}
	return s.mutexLock(ctx, key)
}

// advisoryLock takes a PostgreSQL session advisory lock. The connection is pinned for the
// lifetime of the lock, because the lock belongs to the session: handing the connection
// back to the pool would let another statement unlock it, or leak it into unrelated work.
func (s *store) advisoryLock(ctx context.Context, key string) (func() error, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring a connection to lock %s: %w", key, err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, s.d.rebind(advisoryLockSQL), key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("locking %s: %w", key, err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, fmt.Errorf("locking %s: %w", key, ErrLocked)
	}
	release := func() error {
		// Closing the connection releases the lock server-side whatever the unlock
		// statement does, so it is deferred first.
		defer func() { _ = conn.Close() }()

		unlockCtx, cancel := releaseContext(ctx)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, s.d.rebind(advisoryUnlockSQL), key); err != nil {
			return fmt.Errorf("unlocking %s: %w", key, err)
		}
		return nil
	}
	return release, nil
}

// mutexLock takes the SQLite lock by inserting the mutex row. The insert either creates the
// row or conflicts, and the conflict is the contention: no read-then-write race exists.
func (s *store) mutexLock(ctx context.Context, key string) (func() error, error) {
	holder, err := newHolder()
	if err != nil {
		return nil, err
	}
	inserted, err := s.exec(ctx, mutexLockSQL, key, holder)
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", key, err)
	}
	if inserted == 0 {
		return nil, fmt.Errorf("locking %s: %w", key, ErrLocked)
	}
	release := func() error {
		releaseCtx, cancel := releaseContext(ctx)
		defer cancel()

		deleted, err := s.exec(releaseCtx, mutexUnlockSQL, key, holder)
		if err != nil {
			return fmt.Errorf("unlocking %s: %w", key, err)
		}
		if deleted == 0 {
			return fmt.Errorf("unlocking %s: the lock is now held by someone else", key)
		}
		return nil
	}
	return release, nil
}

// releaseContext detaches from the run's context so that a cancelled run can still release
// its lock, while still bounding how long the attempt may take.
func releaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
}

// newHolder returns a token identifying one lock acquisition, so a release can only remove
// the row it created.
func newHolder() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generating a lock holder token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}
