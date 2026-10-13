package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// cloneSessionTarget copies native receiving state and returns the live snapshot verified during copying.
func cloneSessionTarget(ctx context.Context, item *installItem) (map[string]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	destination := filepath.Join(item.slot, "data")
	if _, err := os.Lstat(item.target); errors.Is(err, os.ErrNotExist) {
		return map[string]fs.FileInfo{}, os.Mkdir(destination, 0700)
	} else if err != nil {
		return nil, err
	}
	before := map[string]fs.FileInfo{}
	directories := []string{}
	// Valid Unix filenames can contain ':' or '\\'; a local clone must not use portable tar extraction.
	err := filepath.WalkDir(item.target, func(source string, _ fs.DirEntry, walkErr error) error {
		if err := errors.Join(ctx.Err(), walkErr); err != nil {
			return err
		}
		// Windows directory enumeration can cache metadata; both snapshots must query the live path.
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		before[source] = info
		relative, err := filepath.Rel(item.target, source)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		switch {
		case info.IsDir():
			directories = append(directories, source)
			return os.Mkdir(target, 0700)
		case info.Mode().IsRegular():
			return cloneSessionFile(ctx, source, target, info)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(source)
			if err != nil {
				return err
			}
			// #nosec G122 -- Target parents are fresh staging directories; links are preserved but never traversed.
			return os.Symlink(link, target)
		case info.Mode()&os.ModeSocket != 0:
			return nil
		default:
			return fmt.Errorf("unsupported receiving file %s", source)
		}
	})
	if err != nil {
		return nil, err
	}
	if err := validateSessionTarget(ctx, item.target, before); err != nil {
		return nil, err
	}
	// Restore restrictive directory permissions after children are copied, deepest directories first.
	for index := len(directories) - 1; index >= 0; index-- {
		source := directories[index]
		relative, err := filepath.Rel(item.target, source)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(destination, relative)
		info := before[source]
		if err := os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
			return nil, err
		}
		if err := os.Chmod(target, info.Mode().Perm()); err != nil {
			return nil, err
		}
	}
	return before, ctx.Err()
}

// validateSessionTarget rejects changes to any copied live entry before its entire root can be replaced.
func validateSessionTarget(ctx context.Context, target string, before map[string]fs.FileInfo) error {
	seen := 0
	err := filepath.WalkDir(target, func(source string, _ fs.DirEntry, walkErr error) error {
		// A previously missing root is unchanged only while it remains absent.
		if source == target && len(before) == 0 && errors.Is(walkErr, os.ErrNotExist) {
			return ctx.Err()
		}
		if err := errors.Join(ctx.Err(), walkErr); err != nil {
			return err
		}
		// Query the live path on every platform, avoiding cached Windows directory metadata.
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		previous, exists := before[source]
		if !exists || !sameSessionFileState(previous, info) {
			return fmt.Errorf("receiving data changed since clone: %s", source)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(before) {
		return errors.New("receiving entries removed since clone")
	}
	return ctx.Err()
}

// sameSessionFileState detects identity, content-size, permission, or timestamp changes in offline local data.
func sameSessionFileState(before, after fs.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() &&
		before.ModTime().Equal(after.ModTime())
}

// cloneSessionFile copies a verified regular file into private staging and retains its local permissions and mtime.
func cloneSessionFile(ctx context.Context, source, target string, info fs.FileInfo) error {
	// #nosec G304 G122 -- Source belongs to a validated offline root; the opened identity is checked below.
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	opened, err := input.Stat()
	if err != nil {
		return err
	}
	if !sameSessionFileState(info, opened) {
		return fmt.Errorf("receiving file replaced before clone: %s", source)
	}
	// #nosec G304 -- Target is within a newly created receiver-owned tree whose ancestors cannot be symlinks.
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(output, contextReader{ctx: ctx, reader: input}, info.Size())
	if err := errors.Join(copyErr, output.Close()); err != nil {
		return err
	}
	if err := os.Chmod(target, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(target, info.ModTime(), info.ModTime())
}
