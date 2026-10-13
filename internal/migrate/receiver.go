package migrate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Receiver accepts one approved migration and preserves all replaced destination roots as backups.
type Receiver struct {
	// Targets maps allowed wire names to destinations selected only by this machine.
	Targets map[string]string
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
	if _, err := r.offerMapper(hello, plan); err != nil {
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
	if err := writeMessage(connection, message{Kind: "ready"}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(r.Output, "Both terminals are ready. Receiving encrypted data...")
	p := newProgress(r.Output, hello.Size)
	hash := sha256.New()
	_, err = io.CopyN(io.MultiWriter(archive, hash, p), networkReader{connection: connection}, hello.Size)
	p.stop()
	if err != nil {
		return fmt.Errorf("receive archive: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != hello.SHA256 {
		return errors.New("archive SHA-256 mismatch; destination untouched")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(r.Output, "SHA-256 verified. Extracting into private staging directories...")
	stopKeepalive := keepAlive(connection)
	mapper, mapErr := r.offerMapper(hello, plan)
	if mapErr != nil {
		stopKeepalive()
		return mapErr
	}
	decoded, closeDecoder, decodeErr := decodeArchive(
		contextReader{ctx: ctx, reader: archive},
		hello.Encoding,
		r.Workers,
	)
	if decodeErr != nil {
		stopKeepalive()
		return decodeErr
	}
	rawSize := hello.RawSize
	if rawSize == 0 {
		rawSize = hello.Size
	}
	bounded := &io.LimitedReader{R: decoded, N: rawSize + 1}
	policy := &extractPolicy{
		archived:     plan.archived,
		mapper:       mapper,
		installation: installation,
		cross:        plan.cross,
		sourcePaths:  hello.Paths,
	}
	err = extractSelected(bounded, installation.resolve, rawSize, policy)
	if err == nil {
		_, err = io.Copy(io.Discard, bounded)
	}
	if err == nil && bounded.N != 1 {
		err = errors.New("decompressed archive does not match its declared size")
	}
	closeDecoder()
	if err == nil && len(mapper.mappings) > 0 {
		err = adaptInstallation(ctx, installation, mapper, r.Workers)
	}
	if err == nil && plan.cross {
		manifest, marshalErr := json.MarshalIndent(hello, "", "  ")
		err = marshalErr
		if err == nil {
			// #nosec G304 -- archiveDirectory is the receiver-owned private preservation slot.
			err = os.WriteFile(filepath.Join(archiveDirectory, "manifest.json"), manifest, 0600)
		}
		if err == nil {
			err = archive.Close()
		}
		if err == nil {
			name := "source.tar"
			if hello.Encoding == "gzip" {
				name += ".gz"
			}
			err = os.Rename(archive.Name(), filepath.Join(archiveDirectory, name))
		}
	}
	if err == nil && r.Check != nil {
		err = r.Check()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = installation.commit()
	}
	stopKeepalive()
	if err != nil {
		return err
	}
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
