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
	"runtime"
	"strings"
	"time"
)

// Sender snapshots local roots and waits for an authenticated receiver to announce readiness.
type Sender struct {
	// Roots identifies all local files and directories included in the migration.
	Roots []Root
	// Output receives connection, progress, and completion status.
	Output io.Writer
	// Fingerprint optionally pins the receiver's full SHA-256 certificate digest.
	Fingerprint string
	// Confirm compares a session-specific pairing code with the other terminal.
	Confirm func(context.Context, string) error
	// Check verifies that Codex is stopped before snapshot and transmission.
	Check func() error
	// Wait limits connection retries; zero selects five minutes.
	Wait time.Duration
}

// Send waits for a receiver, authenticates the session, and returns only after installation acknowledgement.
func (s *Sender) Send(ctx context.Context, address string) error {
	if s.Output == nil {
		s.Output = io.Discard
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Check != nil {
		if err := s.Check(); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(s.Output, "Preparing a consistent local snapshot...")
	bundle, err := SnapshotContext(ctx, s.Roots)
	if err != nil {
		return err
	}
	defer func() { _ = bundle.Close() }()
	_, _ = fmt.Fprintf(
		s.Output,
		"Snapshot: %.2f MiB; skipped runtime sockets: %d\n",
		float64(bundle.Size)/(1<<20),
		bundle.Skipped,
	)
	connection, err := s.connect(ctx, address)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if s.Confirm != nil {
		code, err := pairingCode(connection)
		if err != nil {
			return err
		}
		if err := s.Confirm(ctx, code); err != nil {
			return err
		}
	}
	if s.Check != nil {
		if err := s.Check(); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(s.Roots))
	for _, root := range s.Roots {
		names = append(names, root.Name)
	}
	hello := message{Kind: "offer", OS: runtime.GOOS, Roots: names, Size: bundle.Size, SHA256: bundle.SHA256}
	if err := writeMessage(connection, hello); err != nil {
		return err
	}
	ready, err := readMessage(connection)
	if err != nil {
		return fmt.Errorf("receiver readiness: %w", err)
	}
	if ready.Kind != "ready" {
		return errors.New("receiver did not announce readiness")
	}
	_, _ = fmt.Fprintln(s.Output, "Both terminals are ready. Sending encrypted data...")
	p := newProgress(s.Output, bundle.Size)
	_, err = io.Copy(io.MultiWriter(networkWriter{connection: connection}, p), bundle.File)
	p.stop()
	if err != nil {
		return fmt.Errorf("send archive: %w", err)
	}
	_, _ = fmt.Fprintln(s.Output, "Sent. Waiting for receiver verification and installation...")
	// Receiver extraction may take longer than a network idle period on a large archive.
	for {
		result, err := readMessage(connection)
		if err != nil {
			return fmt.Errorf("installation acknowledgement lost; check receiver before retrying: %w", err)
		}
		if result.Kind == "working" {
			continue
		}
		if result.Kind != "complete" {
			return errors.New("invalid installation acknowledgement")
		}
		break
	}
	_, _ = fmt.Fprintln(s.Output, "Migration complete. Source data retained; destination backups retained.")
	return nil
}

// connect retries a literal IP endpoint and validates an optional certificate pin during TLS negotiation.
func (s *Sender) connect(ctx context.Context, address string) (*tls.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil {
		return nil, fmt.Errorf("expected an IP:port endpoint, got %q", address)
	}
	wait := s.Wait
	if wait == 0 {
		wait = 5 * time.Minute
	}
	if wait < 0 {
		return nil, errors.New("wait duration must be positive")
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	_, _ = fmt.Fprintf(s.Output, "Waiting for receiver at %s (limit %s)...\n", address, wait)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(waitCtx, "tcp", address)
		if err == nil {
			config := &tls.Config{
				MinVersion: tls.VersionTLS13,
				// #nosec G402 -- Ephemeral certificates are verified by a pin or by the CLI
				// pairing callback before sending the offer or any archive data.
				InsecureSkipVerify: true,
				VerifyConnection: func(state tls.ConnectionState) error {
					if len(state.PeerCertificates) != 1 {
						return errors.New("unexpected peer certificate chain")
					}
					hash := sha256.Sum256(state.PeerCertificates[0].Raw)
					actual := hex.EncodeToString(hash[:])
					if s.Fingerprint != "" && !strings.EqualFold(s.Fingerprint, actual) {
						return errors.New("receiver fingerprint mismatch")
					}
					return nil
				},
			}
			connection := tls.Client(raw, config)
			handshakeCtx, stop := context.WithTimeout(waitCtx, 15*time.Second)
			err := connection.HandshakeContext(handshakeCtx)
			stop()
			if err != nil {
				_ = raw.Close()
				return nil, fmt.Errorf("TLS handshake: %w", err)
			}
			return connection, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("receiver not ready: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
