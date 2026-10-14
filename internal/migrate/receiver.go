package migrate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Receiver accepts one approved migration and preserves all replaced destination roots as backups.
type Receiver struct {
	// Targets maps allowed wire names to destinations selected only by this machine.
	Targets map[string]string
	// SQLiteHome selects local session state independently of separate destination roots.
	SQLiteHome string
	// Output receives pairing, transfer, validation, and backup status.
	Output io.Writer
	// MaxBytes bounds archive storage and extracted regular file payloads.
	MaxBytes int64
	// Confirm requires human verification of the TLS session on the receiving terminal.
	Confirm func(context.Context, string) error
	// Check verifies Codex is stopped before readiness and before installation.
	Check func() error
	// Home selects the local home prefix used when adapting structured state paths.
	Home string
	// ImportRoot retains complete unchanged snapshots from foreign operating systems.
	ImportRoot string
	// Overrides contains only explicitly supplied destination root mappings.
	Overrides map[string]string
	// PathMappings overrides automatic prefix translation for external project locations.
	PathMappings map[string]string
	// Workers selects CPU workers for decompression and independent metadata files.
	Workers int
	// certificate is ephemeral and never written to disk.
	certificate tls.Certificate
	// fingerprint is the full SHA-256 digest of the prepared certificate.
	fingerprint string
}

// Prepare validates local configuration and creates the TLS identity before listening.
func (r *Receiver) Prepare() error {
	if r.Output == nil {
		r.Output = io.Discard
	}
	if r.MaxBytes <= 0 {
		return errors.New("maximum archive size must be positive")
	}
	if len(r.Targets) == 0 {
		return errors.New("no receiver destinations configured")
	}
	roots := make([]Root, 0, len(r.Targets))
	for name, path := range r.Targets {
		roots = append(roots, Root{Name: name, Path: path})
	}
	if err := ValidateRoots(roots); err != nil {
		return err
	}
	if r.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		r.Home = home
	}
	if r.ImportRoot == "" {
		r.ImportRoot = filepath.Join(r.Home, ".codex-transfer", "imports")
	}
	if _, err := workerCount(r.Workers); err != nil {
		return err
	}
	certificate, fingerprint, err := ephemeralCertificate()
	if err != nil {
		return err
	}
	r.certificate, r.fingerprint = certificate, fingerprint
	return nil
}

// Fingerprint returns the digest displayed locally for optional certificate pinning.
func (r *Receiver) Fingerprint() string { return r.fingerprint }

// Serve waits for one TLS connection and closes network resources promptly on cancellation.
func (r *Receiver) Serve(ctx context.Context, listener net.Listener) error {
	if r.fingerprint == "" {
		return errors.New("receiver must be prepared before listening")
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	_, _ = fmt.Fprintf(
		r.Output,
		"Receiver listening at %s\nCertificate SHA-256: %s\nWaiting for sender...\n",
		listener.Addr(),
		r.fingerprint,
	)
	for {
		raw, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		connection := tls.Server(
			raw,
			&tls.Config{Certificates: []tls.Certificate{r.certificate}, MinVersion: tls.VersionTLS13},
		)
		closeOnCancel := context.AfterFunc(ctx, func() { _ = connection.Close() })
		handshakeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = connection.HandshakeContext(handshakeCtx)
		cancel()
		if err != nil {
			closeOnCancel()
			_ = connection.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			_, _ = fmt.Fprintln(r.Output, "Rejected invalid TLS connection.")
			continue
		}
		_, _ = fmt.Fprintf(r.Output, "Sender connected from %s\n", connection.RemoteAddr())
		err = r.receive(ctx, connection)
		if err != nil {
			_ = writeMessage(connection, message{Kind: "error", Error: err.Error()})
		}
		closeOnCancel()
		_ = connection.Close()
		return err
	}
}

// receive gates archive bytes on approval, target validation, staging allocation, and readiness.
func (r *Receiver) receive(ctx context.Context, connection *tls.Conn) error {
	if r.Confirm != nil {
		code, err := pairingCode(connection)
		if err != nil {
			return err
		}
		if err := r.Confirm(ctx, code); err != nil {
			return err
		}
	}
	hello, err := readMessage(connection)
	if err != nil {
		return err
	}
	plan, err := r.validateOffer(hello)
	if err != nil {
		return err
	}
	// Keep the validated mapping for extraction and adaptation instead of rebuilding it after transfer.
	mapper, err := r.offerMapper(hello, plan)
	if err != nil {
		return err
	}
	if r.Check != nil {
		if err := r.Check(); err != nil {
			return err
		}
	}
	if plan.cross {
		_, _ = fmt.Fprintf(
			r.Output,
			"Cross-system import: %s -> %s. An unchanged source snapshot will be preserved.\n",
			hello.OS,
			runtime.GOOS,
		)
		for _, name := range hello.Roots {
			if plan.archived[name] {
				_, _ = fmt.Fprintf(r.Output, "Preserving foreign root in source archive: %s\n", name)
			}
		}
	}
	installation, err := prepareInstallation(plan.roots)
	if err != nil {
		return err
	}
	defer installation.close()
	installation.sqliteHome = plan.sqliteHome
	archiveDirectory := installation.items[0].slot
	if plan.cross {
		archiveDirectory, err = installation.resolve(sourceArchiveRoot)
		if err != nil {
			return err
		}
		if err := os.Mkdir(archiveDirectory, 0700); err != nil {
			return err
		}
	}
	archive, err := os.CreateTemp(archiveDirectory, "source-*")
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	if err := r.receiveArchive(connection, archive, hello); err != nil {
		return err
	}
	// Deferred shutdown covers every failure; the explicit stop keeps working messages before the acknowledgement.
	stopKeepalive := sync.OnceFunc(keepAlive(connection))
	defer stopKeepalive()
	policy := &extractPolicy{
		archived:     plan.archived,
		mapper:       mapper,
		installation: installation,
		cross:        plan.cross,
		sourcePaths:  hello.Paths,
	}
	if err := r.extractArchive(ctx, archive, hello, policy); err != nil {
		return err
	}
	if len(mapper.mappings) > 0 {
		if hello.Mode == "sessions" {
			err = adaptSessionInstallation(ctx, installation, mapper, r.Workers)
		} else {
			err = adaptInstallation(ctx, installation, mapper, r.Workers)
		}
		if err != nil {
			return err
		}
	}
	if hello.Mode == "sessions" {
		if err := mergeSessionInstallation(ctx, installation, r.Output); err != nil {
			return err
		}
	}
	if plan.cross {
		if err := preserveSourceArchive(archive, hello); err != nil {
			return err
		}
	}
	if r.Check != nil {
		if err := r.Check(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Recheck the cloned roots after the final process check to preserve concurrent edits.
	if err := installation.validateSessionTargets(ctx); err != nil {
		return err
	}
	if err := installation.commit(); err != nil {
		return err
	}
	stopKeepalive()
	for _, item := range installation.items {
		_, _ = fmt.Fprintf(r.Output, "Installed %s: %s\n", item.name, item.target)
		if item.backup != "" {
			_, _ = fmt.Fprintf(r.Output, "Backup: %s\n", item.backup)
		}
	}
	if err := writeMessage(connection, message{Kind: "complete"}); err != nil {
		return fmt.Errorf("installed successfully but acknowledgement failed: %w", err)
	}
	_, _ = fmt.Fprintln(r.Output, "Migration complete. Restart Codex after checking project paths and authentication.")
	return nil
}

// validateOffer validates platform metadata and plans active imports plus source preservation.
func (r *Receiver) validateOffer(hello message) (*receivePlan, error) {
	if hello.Mode != "" && hello.Mode != "sessions" {
		return nil, errors.New("unsupported transfer mode")
	}
	if hello.Mode == "sessions" {
		if hello.Version != sessionProtocolVersion || len(hello.Roots) != 1 || hello.Roots[0] != "codex" {
			return nil, errors.New("session merging requires protocol 3 and only the Codex root")
		}
	} else if hello.Version == sessionProtocolVersion {
		return nil, errors.New("protocol 3 requires session merging")
	}
	if hello.Kind != "offer" || !supportedOS(hello.OS) {
		return nil, errors.New("invalid offer or unsupported source platform")
	}
	if hello.Encoding != "" && hello.Encoding != "none" && hello.Encoding != "gzip" {
		return nil, errors.New("unsupported archive encoding")
	}
	if hello.RawSize < 0 || hello.RawSize > r.MaxBytes || hello.RawSize == 0 && hello.Encoding == "gzip" {
		return nil, errors.New("invalid decompressed archive size")
	}
	if hello.Size <= 0 || hello.Size > r.MaxBytes {
		return nil, errors.New("archive exceeds receiver size limit")
	}
	digest, err := hex.DecodeString(hello.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("invalid archive checksum")
	}
	if len(hello.Roots) == 0 || len(hello.Roots) > 64 {
		return nil, errors.New("invalid root count")
	}
	plan, err := planDestinations(hello.OS, runtime.GOOS, hello.Roots, r.Targets, r.Overrides, r.ImportRoot)
	if err != nil {
		return nil, err
	}
	if hello.Mode == "sessions" {
		codexHome := ""
		for _, root := range plan.roots {
			if root.Name == "codex" {
				codexHome = root.Path
			}
		}
		override := r.SQLiteHome
		if override == "" {
			override = r.Overrides["sqlite"]
		}
		if override == "" {
			override = r.Targets["sqlite"]
		}
		// Internal overrides need no separate root, but still select the database used for session merging.
		target, err := sessionSQLiteHome(codexHome, override)
		if err != nil {
			return nil, err
		}
		if !within(codexHome, target) {
			if _, err := os.Lstat(target); err == nil {
				plan.roots = append(plan.roots, Root{Name: "sqlite", Path: target})
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if err := ValidateRoots(plan.roots); err != nil {
			return nil, err
		}
		plan.sqliteHome = target
	}
	if plan.cross && (hello.Home == "" || len(hello.Paths) != len(hello.Roots)) {
		return nil, errors.New("cross-system migration requires complete source path metadata")
	}
	for name, source := range hello.Paths {
		known := false
		for _, root := range hello.Roots {
			known = known || root == name
		}
		if !known || sourcePath(source, hello.OS) == "" {
			return nil, errors.New("invalid source root path metadata")
		}
	}
	for key, value := range hello.StatePaths {
		if (key != "sqlite_home" && key != "log_dir") || sourcePath(value, hello.OS) == "" {
			return nil, errors.New("invalid source state path metadata")
		}
	}
	return plan, nil
}

// offerMapper combines automatic root/home mappings with explicit receiver-side project overrides.
func (r *Receiver) offerMapper(hello message, plan *receivePlan) (*pathMapper, error) {
	mappings := map[string]string{}
	if hello.Home != "" {
		mappings[hello.Home] = r.Home
	}
	for _, root := range plan.roots {
		if source := hello.Paths[root.Name]; source != "" {
			mappings[source] = root.Path
		}
	}
	for source, target := range r.PathMappings {
		mappings[source] = target
	}
	mapper, err := newPathMapper(hello.OS, mappings)
	if err != nil {
		return nil, err
	}
	if hello.Mode == "sessions" {
		return mapper, nil
	}
	for key, source := range hello.StatePaths {
		mapper.statePaths[key] = mapper.remap(source)
	}
	for _, root := range plan.roots {
		switch root.Name {
		case "sqlite":
			mapper.statePaths["sqlite_home"] = root.Path
		case "logs":
			mapper.statePaths["log_dir"] = root.Path
		}
	}
	return mapper, nil
}

// keepAlive prevents network inactivity timeouts while local archive extraction is running.
func keepAlive(connection net.Conn) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := writeMessage(connection, message{Kind: "working"}); err != nil {
					return
				}
			}
		}
	}()
	return func() { close(done); <-finished }
}
