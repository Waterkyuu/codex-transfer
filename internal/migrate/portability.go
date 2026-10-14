package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// replaceStaged publishes translated private staging metadata with the source modification time.
func replaceStaged(file, destination string, transform func(io.Reader, io.Writer) error) error {
	// #nosec G304 G122 -- These paths belong to validated private staging, never the live destination.
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(file), ".translated-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(output.Name()) }()
	err = errors.Join(transform(input, output), input.Close(), output.Close())
	if err != nil {
		return err
	}
	// Fresh temporary files otherwise make old imported conversations appear newly active in the session index.
	if err := os.Chtimes(output.Name(), info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	return os.Rename(output.Name(), destination)
}

// translateMetadata handles only Codex-owned config, session, global-state, and SQLite files.
func translateMetadata(ctx context.Context, file string, mapper *pathMapper) error {
	base := filepath.Base(file)
	if strings.HasSuffix(base, ".sqlite") {
		return translateDatabase(ctx, file, mapper)
	}
	if strings.HasSuffix(base, ".jsonl.zst") {
		return translateCompressedRollout(ctx, file, mapper)
	}
	return replaceStaged(file, file, func(input io.Reader, output io.Writer) error {
		input = contextReader{ctx: ctx, reader: input}
		if strings.HasSuffix(base, ".jsonl") {
			return translateRollouts(input, output, mapper)
		}
		data, err := io.ReadAll(io.LimitReader(input, (16<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 16<<20 {
			return errors.New("structured metadata exceeds the 16 MiB conversion limit")
		}
		if base == "config.toml" {
			data, err = translateConfig(data, mapper)
		} else {
			data, err = translateJSON(data, mapper)
		}
		if err != nil {
			return err
		}
		_, err = output.Write(data)
		return err
	})
}

// adaptInstallation parallelizes independent metadata conversions before any live root is replaced.
func adaptInstallation(ctx context.Context, installation *installation, mapper *pathMapper, workers int) error {
	workers, err := workerCount(workers)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	failures := make(chan error, 1)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for file := range jobs {
				if err := ctx.Err(); err != nil {
					return
				}
				if err := translateMetadata(ctx, file, mapper); err != nil {
					select {
					case failures <- fmt.Errorf("translate %s: %w", filepath.Base(file), err):
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	sqliteHome := mapper.statePaths["sqlite_home"]
	if sqliteHome == "" {
		for _, item := range installation.items {
			if item.name == "codex" {
				sqliteHome = item.target
			}
			if item.name == "sqlite" {
				sqliteHome = item.target
				break
			}
		}
	}
	databases := []string{}
	for _, item := range installation.items {
		if item.name != "codex" && item.name != "sqlite" {
			continue
		}
		err = filepath.WalkDir(
			filepath.Join(item.slot, "data"),
			func(file string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if !entry.Type().IsRegular() {
					return nil
				}
				relative, err := filepath.Rel(filepath.Join(item.slot, "data"), file)
				if err != nil {
					return err
				}
				base := entry.Name()
				// Windows directory casing must not exclude databases owned by the configured SQLite home.
				databaseDirectory, directoryErr := filepath.Rel(
					sqliteHome,
					filepath.Dir(filepath.Join(item.target, relative)),
				)
				isDatabase := ownedDatabaseName(base) && directoryErr == nil && databaseDirectory == "."
				portablePath := filepath.ToSlash(relative)
				isRootMetadata := portablePath == "config.toml" || portablePath == ".codex-global-state.json"
				isRollout := strings.HasSuffix(base, ".jsonl") || strings.HasSuffix(base, ".jsonl.zst")
				inSessionDirectory := strings.HasPrefix(portablePath, "sessions/") ||
					strings.HasPrefix(portablePath, "archived_sessions/")
				// Project and plugin trees may contain identical names; ownership follows root-relative locations.
				selected := isDatabase || item.name == "codex" && (isRootMetadata || isRollout && inSessionDirectory)
				if !selected {
					return nil
				}
				if isDatabase {
					databases = append(databases, file)
					return nil
				}
				if strings.HasSuffix(base, ".jsonl.zst") {
					destination := filepath.Join(item.target, relative)
					mapper.rollouts[destination] = strings.TrimSuffix(destination, ".zst")
				}
				select {
				case jobs <- file:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		)
		if err != nil {
			break
		}
	}
	close(jobs)
	group.Wait()
	select {
	case failure := <-failures:
		return failure
	default:
		if err := errors.Join(err, ctx.Err()); err != nil {
			return err
		}
	}
	if err := ensureStateConfig(installation, mapper); err != nil {
		return err
	}
	// A single SQLite engine per database avoids duplicate writers; independent metadata already ran in parallel.
	for _, file := range databases {
		if err := translateDatabase(ctx, file, mapper); err != nil {
			return fmt.Errorf("translate %s: %w", filepath.Base(file), err)
		}
	}
	return nil
}

// ownedDatabaseName recognizes Codex database families without interpreting arbitrary project SQLite files.
func ownedDatabaseName(name string) bool {
	if !strings.HasSuffix(name, ".sqlite") {
		return false
	}
	stem := strings.TrimSuffix(name, ".sqlite")
	for _, family := range []string{"state", "logs", "thread_history"} {
		if stem == family {
			return true
		}
		version, ok := strings.CutPrefix(stem, family+"_")
		if !ok || version == "" {
			continue
		}
		for _, char := range version {
			if char < '0' || char > '9' {
				return false
			}
		}
		return true
	}
	return false
}
