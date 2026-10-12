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
	roots, err := r.validateOffer(hello)
	if err != nil {
		return err
	}
	if r.Check != nil {
		if err := r.Check(); err != nil {
			return err
		}
	}
	installation, err := prepareInstallation(roots)
	if err != nil {
		return err
	}
	defer installation.close()
	archive, err := os.CreateTemp(installation.items[0].slot, "archive-*")
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
	err = extractInto(contextReader{ctx: ctx, reader: archive}, installation.resolve, hello.Size)
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

// validateOffer permits only bounded same-platform archives with explicitly mapped unique roots.
func (r *Receiver) validateOffer(hello message) ([]Root, error) {
	if hello.Kind != "offer" || hello.OS != runtime.GOOS {
		return nil, errors.New("invalid offer or unsupported cross-platform migration")
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
	roots := make([]Root, 0, len(hello.Roots))
	for _, name := range hello.Roots {
		path, ok := r.Targets[name]
		if !ok {
			return nil, fmt.Errorf("root %q has no destination; restart receiver with --map %s=/path", name, name)
		}
		roots = append(roots, Root{Name: name, Path: path})
	}
	return roots, ValidateRoots(roots)
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
