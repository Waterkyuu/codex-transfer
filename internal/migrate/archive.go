package migrate

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Bundle owns a private disk-backed archive and its integrity metadata.
type Bundle struct {
	// File is rewound for transmission after the snapshot has been validated.
	File *os.File
	// Size is the exact number of transmitted archive bytes.
	Size int64
	// RawSize bounds the uncompressed tar stream independently of network compression.
	RawSize int64
	// Encoding identifies the archive compression contract.
	Encoding string
	// SHA256 authenticates the complete archive before installation.
	SHA256 string
	// Skipped counts runtime sockets, which cannot be restored as persistent files.
	Skipped int
}

// Close removes sensitive snapshot data after closing the underlying file.
func (b *Bundle) Close() error {
	return errors.Join(b.File.Close(), os.Remove(b.File.Name()))
}

// Snapshot creates a disk-backed archive and rejects files changed during the snapshot.
func Snapshot(roots []Root) (*Bundle, error) {
	return SnapshotContext(context.Background(), roots)
}

// SnapshotContext allows cancellation while scanning and copying large local data files.
func SnapshotContext(ctx context.Context, roots []Root) (*Bundle, error) {
	return snapshot(ctx, roots, 0, "none")
}

// snapshot overlaps tar construction, parallel compression, disk output, and digest calculation.
func snapshot(ctx context.Context, roots []Root, workers int, compression string) (*Bundle, error) {
	return snapshotFiles(ctx, roots, workers, compression, nil)
}

// snapshotFiles optionally confines archive entries to a previously inspected selection and its ancestors.
func snapshotFiles(
	ctx context.Context,
	roots []Root,
	workers int,
	compression string,
	selected map[string]fs.FileInfo,
) (*Bundle, error) {
	if len(roots) == 0 {
		return nil, errors.New("no source data found")
	}
	if err := ValidateRoots(roots); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "codex-transfer-snapshot-*")
	if err != nil {
		return nil, err
	}
	bundle := &Bundle{File: file, Encoding: "none"}
	success := false
	defer func() {
		if !success {
			_ = bundle.Close()
		}
	}()
	hash := sha256.New()
	output := io.MultiWriter(file, hash)
	var codec *compressionWriter
	if compression != "none" {
		codec, err = newCompressionWriter(ctx, output, workers, compression)
		if err != nil {
			return nil, err
		}
		defer func() { _ = codec.Close() }()
		output = codec
		bundle.Encoding = "gzip"
	}
	counter := &countWriter{writer: output}
	writer := tar.NewWriter(counter)
	before := map[string]fs.FileInfo{}
	for _, root := range roots {
		err := filepath.WalkDir(root.Path, func(local string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if selected != nil {
				if _, included := selected[local]; !included {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			// Match session selection's Lstat snapshots instead of cached Windows enumeration metadata.
			info, err := os.Lstat(local)
			if err != nil {
				return err
			}
			if old := selected[local]; old != nil && (!os.SameFile(old, info) || old.Size() != info.Size() ||
				!old.ModTime().Equal(info.ModTime()) || old.Mode() != info.Mode()) {
				return fmt.Errorf("source changed after session selection: %s", local)
			}
			before[local] = info
			if info.Mode()&os.ModeSocket != 0 {
				bundle.Skipped++
				return nil
			}
			allowed := info.Mode().IsRegular() || info.IsDir() || info.Mode()&os.ModeSymlink != 0
			if !allowed {
				return fmt.Errorf("unsupported special file %s", local)
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(local)
				if err != nil {
					return err
				}
			}
			header, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root.Path, local)
			if err != nil {
				return err
			}
			header.Name = path.Join(root.Name, filepath.ToSlash(rel))
			// Ownership is always that of the receiving user; do not replay source UIDs.
			header.Uid, header.Gid = 0, 0
			header.Uname, header.Gname = "", ""
			if err := writer.WriteHeader(header); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			// #nosec G304 G122 -- Go 1.22 has no os.Root API; this reads a trusted, user-selected
			// offline tree. The opened file identity and the complete tree metadata are
			// checked before the snapshot is eligible for transmission.
			input, err := os.Open(local)
			if err != nil {
				return err
			}
			opened, err := input.Stat()
			if err == nil && !os.SameFile(info, opened) {
				err = fmt.Errorf("source replaced: %s", local)
			}
			if err == nil {
				_, err = io.CopyN(writer, contextReader{ctx: ctx, reader: input}, info.Size())
			}
			return errors.Join(err, input.Close())
		})
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("snapshot %s: %w", root.Name, err)
		}
	}
	if selected != nil && len(before) != len(selected) {
		_ = writer.Close()
		return nil, errors.New("selected source entries removed before snapshot")
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if codec != nil {
		if err := codec.Close(); err != nil {
			return nil, err
		}
	}
	bundle.RawSize = counter.total
	after := map[string]fs.FileInfo{}
	for _, root := range roots {
		if err := filepath.WalkDir(root.Path, func(local string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if selected != nil {
				if _, included := selected[local]; !included {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			info, err := os.Lstat(local)
			if err != nil {
				return err
			}
			after[local] = info
			old, ok := before[local]
			if !ok || !os.SameFile(old, info) || old.Size() != info.Size() || old.Mode() != info.Mode() ||
				!old.ModTime().Equal(info.ModTime()) {
				return fmt.Errorf("source changed during snapshot: %s", local)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if len(before) != len(after) {
		return nil, errors.New("source entries removed during snapshot")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	bundle.Size = info.Size()
	bundle.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	success = true
	return bundle, nil
}

// countWriter measures raw tar bytes without storing an extra uncompressed snapshot.
type countWriter struct {
	// writer receives the streamed archive or compression input.
	writer io.Writer
	// total is the exact successfully written uncompressed byte count.
	total int64
}

// Write counts accepted bytes while preserving the underlying writer's failures.
func (w *countWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.total += int64(n)
	return n, err
}

// contextReader checks cancellation between bounded file read operations.
type contextReader struct {
	// ctx belongs to the enclosing snapshot or extraction operation.
	ctx context.Context
	// reader supplies local disk bytes without network blocking.
	reader io.Reader
}

// Read prevents a canceled operation from continuing to copy local payloads.
func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// Extract restores a validated archive into a fresh directory without following archive symlinks.
func Extract(reader io.Reader, directory string, maxBytes int64) error {
	return extractInto(
		reader,
		func(name string) (string, error) { return filepath.Join(directory, name), nil },
		maxBytes,
	)
}

// pendingLink delays symlink creation until regular file extraction has finished.
type pendingLink struct {
	// path is the staging location of the new symbolic link.
	path string
	// target is preserved literally and is never traversed by the extractor.
	target string
	// root identifies the source root for relative symlink translation.
	root string
	// relative locates this link inside that source root.
	relative string
}

// extractInto confines each archive root to a receiver-owned staging location.
func extractInto(reader io.Reader, resolve func(string) (string, error), maxBytes int64) error {
	return extractSelected(reader, resolve, maxBytes, nil)
}

// extractSelected restores portable roots and validates preserved roots without activating their contents.
func extractSelected(
	reader io.Reader,
	resolve func(string) (string, error),
	maxBytes int64,
	policy *extractPolicy,
) error {
	if maxBytes <= 0 {
		return errors.New("archive byte limit must be positive")
	}
	archive := tar.NewReader(reader)
	seen := map[string]byte{}
	caseNames := map[string]bool{}
	links := []pendingLink{}
	var extracted int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		root, rel, _ := strings.Cut(name, "/")
		archived := policy != nil && policy.archived[root]
		unsafe := path.Clean(name) != name || path.IsAbs(name) || strings.ContainsRune(name, 0)
		if !archived {
			unsafe = unsafe || strings.ContainsAny(name, "\\:") || !filepath.IsLocal(filepath.FromSlash(name))
		}
		if unsafe {
			return fmt.Errorf("unsafe archive path %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate archive path %q", name)
		}
		if !validName(root) {
			return fmt.Errorf("invalid archive root %q", root)
		}
		if rel == "" && header.Typeflag == tar.TypeSymlink {
			return fmt.Errorf("symbolic archive root %q", root)
		}
		if header.Size < 0 || header.Size > maxBytes-extracted {
			return errors.New("archive exceeds extraction limit")
		}
		extracted += header.Size
		if archived {
			seen[name] = header.Typeflag
			continue
		}
		if runtime.GOOS == "windows" {
			if err := windowsArchiveName(name); err != nil {
				return err
			}
			canonical := strings.ToLower(name)
			if caseNames[canonical] {
				return fmt.Errorf("archive names collide on Windows: %q", name)
			}
			caseNames[canonical] = true
		}
		base, err := resolve(root)
		if err != nil {
			return err
		}
		local := filepath.Join(base, filepath.FromSlash(rel))
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if kind, ok := seen[ancestor]; !ok || kind != tar.TypeDir {
				return fmt.Errorf("non-directory ancestor %q", ancestor)
			}
		}
		seen[name] = header.Typeflag
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(local, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			// #nosec G304 -- Archive paths and their ancestors have been validated and resolve only into private staging.
			file, err := os.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode&0700))
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
			if err := os.Chtimes(local, header.ModTime, header.ModTime); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if strings.ContainsRune(header.Linkname, 0) {
				return errors.New("invalid symlink target")
			}
			links = append(links, pendingLink{path: local, target: header.Linkname, root: root, relative: rel})
		default:
			return fmt.Errorf("unsupported archive entry type %d", header.Typeflag)
		}
	}
	if len(seen) == 0 {
		return errors.New("empty archive")
	}
	for _, link := range links {
		if policy != nil && policy.cross && runtime.GOOS == "windows" {
			if err := policy.materializeLink(link, links, map[string]bool{}); err != nil {
				return err
			}
			continue
		}
		target := link.target
		if policy != nil && policy.mapper != nil {
			target = policy.mapper.remap(target)
			if target == link.target && policy.mapper.sourceOS == "windows" && policy.cross {
				target = filepath.FromSlash(strings.ReplaceAll(target, `\`, "/"))
			}
		}
		if err := os.Symlink(target, link.path); err != nil {
			return err
		}
	}
	return nil
}
