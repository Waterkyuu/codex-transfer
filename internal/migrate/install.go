package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// installation owns sibling staging locations and backups for one migration transaction.
type installation struct {
	// items retain deterministic replacement order and rollback state.
	items []*installItem
	// sqliteHome is the receiver-selected live SQLite directory mapped into private staging for session imports.
	sqliteHome string
	// closed makes cleanup idempotent without removing another operation's later lock.
	closed bool
}

// installItem describes one locally selected destination and its recoverable replacement.
type installItem struct {
	// name selects a root from the authenticated archive manifest.
	name string
	// target is the live destination file or directory.
	target string
	// slot is a private temporary directory on the destination filesystem.
	slot string
	// backup keeps the original destination after success.
	backup string
	// installed records whether the staged data reached its live path.
	installed bool
	// lock holds exclusive migration ownership for the local target.
	lock *os.File
}

// prepareInstallation allocates staging on each destination filesystem before readiness is announced.
func prepareInstallation(roots []Root) (*installation, error) {
	if err := ValidateRoots(roots); err != nil {
		return nil, err
	}
	installation := &installation{items: make([]*installItem, 0, len(roots))}
	for _, root := range roots {
		parent := filepath.Dir(root.Path)
		if err := os.MkdirAll(parent, 0700); err != nil {
			installation.close()
			return nil, err
		}
		lock, err := os.OpenFile(root.Path+".transfer-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			installation.close()
			return nil, fmt.Errorf("acquire migration lock for %s: %w", root.Path, err)
		}
		slot, err := os.MkdirTemp(parent, ".codex-transfer-*")
		if err != nil {
			_ = lock.Close()
			_ = os.Remove(lock.Name())
			installation.close()
			return nil, err
		}
		installation.items = append(
			installation.items,
			&installItem{name: root.Name, target: root.Path, slot: slot, lock: lock},
		)
	}
	return installation, nil
}

// resolve returns only preallocated staging paths, ignoring remote source paths.
func (i *installation) resolve(name string) (string, error) {
	for _, item := range i.items {
		if item.name == name {
			return filepath.Join(item.slot, "data"), nil
		}
	}
	return "", fmt.Errorf("unmapped archive root %q", name)
}

// close removes staging artifacts; successful backups intentionally remain.
func (i *installation) close() {
	if i.closed {
		return
	}
	i.closed = true
	for _, item := range i.items {
		_ = os.RemoveAll(item.slot)
		_ = item.lock.Close()
		_ = os.Remove(item.lock.Name())
	}
}

// commit replaces roots with sibling renames and rolls back all completed replacements on failure.
func (i *installation) commit() error {
	for _, item := range i.items {
		if _, err := os.Lstat(filepath.Join(item.slot, "data")); err != nil {
			return fmt.Errorf("missing root %s: %w", item.name, err)
		}
	}
	for _, item := range i.items {
		if _, err := os.Lstat(item.target); err == nil {
			// The random staging suffix also reserves a unique adjacent backup name.
			backup := item.target + ".backup-" + filepath.Base(item.slot)
			if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
				return i.fail(fmt.Errorf("backup path already exists: %s", backup))
			}
			if err := os.Rename(item.target, backup); err != nil {
				return i.fail(err)
			}
			item.backup = backup
		} else if !errors.Is(err, os.ErrNotExist) {
			return i.fail(err)
		}
		if err := os.Rename(filepath.Join(item.slot, "data"), item.target); err != nil {
			return i.fail(err)
		}
		item.installed = true
	}
	return nil
}

// fail restores displaced roots and reports any rollback error with recoverable paths.
func (i *installation) fail(cause error) error {
	for index := len(i.items) - 1; index >= 0; index-- {
		item := i.items[index]
		if item.installed {
			if err := os.Rename(item.target, filepath.Join(item.slot, "failed")); err != nil {
				cause = errors.Join(cause, fmt.Errorf("rollback %s; backup %s: %w", item.target, item.backup, err))
				continue
			}
		}
		if item.backup != "" {
			if err := os.Rename(item.backup, item.target); err != nil {
				cause = errors.Join(cause, fmt.Errorf("restore backup %s: %w", item.backup, err))
			}
		}
	}
	return cause
}
