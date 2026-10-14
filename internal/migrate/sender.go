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
	// Workers selects parallel compression workers; zero uses the runtime CPU budget.
	Workers int
	// Compression selects fast, balanced, best, or none; the default favors throughput.
	Compression string
	// SQLiteHome selects source state independently of whether it needs a separate transfer root.
	SQLiteHome string
	// Filter selects session-only merging; nil retains complete root migration.
	Filter *SessionFilter
}

// Send waits for a receiver, authenticates the session, and returns only after installation acknowledgement.
func (s *Sender) Send(ctx context.Context, address string) error {
	started := time.Now()
	for _, root := range s.Roots {
		if root.Name == sourceArchiveRoot {
			return errors.New("source root name is reserved for receiver preservation")
		}
	}
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
	roots := s.Roots
	var bundle *Bundle
	var err error
	if s.Filter != nil {
		roots = []Root{}
		sqliteHome := s.SQLiteHome
		for _, root := range s.Roots {
			if root.Name == "sqlite" && sqliteHome == "" {
				sqliteHome = root.Path
			}
			if root.Name == "codex" {
				absolute, err := filepath.Abs(root.Path)
				if err != nil {
					return err
				}
				root.Path = absolute
				roots = append(roots, root)
			}
		}
		if len(roots) != 1 {
			return errors.New("session filtering requires exactly one Codex source root")
		}
		if err := ValidateRoots(roots); err != nil {
			return err
		}
		selected, count, selectionErr := s.Filter.selectFiles(ctx, roots[0].Path, sqliteHome)
		if selectionErr != nil {
			return selectionErr
		}
		_, _ = fmt.Fprintf(
			s.Output,
			"Selected sessions: %d. Only rollouts will be sent; receiving sessions will be merged.\n",
			count,
		)
		bundle, err = snapshotFiles(ctx, roots, s.Workers, s.Compression, selected)
	} else {
		bundle, err = snapshot(ctx, roots, s.Workers, s.Compression)
	}
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
	_, _ = fmt.Fprintf(s.Output, "Archive encoding: %s; raw: %.2f MiB; network: %.2f MiB\n",
		bundle.Encoding, float64(bundle.RawSize)/(1<<20), float64(bundle.Size)/(1<<20))
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
	statePaths := map[string]string{}
	names := make([]string, 0, len(roots))
	paths := make(map[string]string, len(roots))
	for _, root := range roots {
		names = append(names, root.Name)
		absolute, err := filepath.Abs(root.Path)
		if err != nil {
			return err
		}
		paths[root.Name] = absolute
		if root.Name == "codex" && s.Filter == nil {
			configured, err := configuredPaths(filepath.Join(root.Path, "config.toml"))
			if err != nil {
				return err
			}
			sqliteHome, err := effectiveSQLiteHome(root.Path, s.SQLiteHome, configured)
			if err != nil {
				return err
			}
			// Internal state travels inside the Codex root but still needs its effective selector persisted.
			if sqliteHome != absolute || configured["sqlite_home"] != "" {
				configured["sqlite_home"] = sqliteHome
			}
			for key, value := range configured {
				statePaths[key], err = filepath.Abs(value)
				if err != nil {
					return err
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	hello := message{
		Kind: "offer", OS: runtime.GOOS, Home: home, Paths: paths, StatePaths: statePaths, Roots: names,
		Encoding: bundle.Encoding, RawSize: bundle.RawSize, Size: bundle.Size, SHA256: bundle.SHA256,
	}
	if s.Filter != nil {
		hello.Version, hello.Mode = sessionProtocolVersion, "sessions"
	}
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
	if s.Filter != nil && (ready.Version != sessionProtocolVersion || ready.Mode != "sessions") {
		return errors.New("receiver does not support session merging; upgrade codex-transfer on both computers")
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
	elapsed := time.Since(started).Seconds()
	_, _ = fmt.Fprintf(
		s.Output,
		"Overall: %.2f MiB of source data in %.2fs (%.2f MiB/s, including preparation and installation).\n",
		float64(bundle.RawSize)/(1<<20),
		elapsed,
		float64(bundle.RawSize)/(1<<20)/elapsed,
	)
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
