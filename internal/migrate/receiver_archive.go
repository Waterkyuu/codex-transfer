package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
)

// receiveArchive announces readiness and verifies the complete encrypted payload before extraction.
func (r *Receiver) receiveArchive(connection net.Conn, archive *os.File, hello message) error {
	if err := writeMessage(connection, message{Kind: "ready", Version: hello.Version, Mode: hello.Mode}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(r.Output, "Both terminals are ready. Receiving encrypted data...")
	p := newProgress(r.Output, hello.Size)
	hash := sha256.New()
	_, err := io.CopyN(io.MultiWriter(archive, hash, p), networkReader{connection: connection}, hello.Size)
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
	return nil
}

// extractArchive owns the decoder and enforces declared archive sizes in private staging.
func (r *Receiver) extractArchive(
	ctx context.Context,
	archive *os.File,
	hello message,
	policy *extractPolicy,
) error {
	decoded, closeDecoder, err := decodeArchive(contextReader{ctx: ctx, reader: archive}, hello.Encoding, r.Workers)
	if err != nil {
		return err
	}
	defer closeDecoder()
	rawSize := hello.RawSize
	if rawSize == 0 {
		rawSize = hello.Size
	}
	bounded := &io.LimitedReader{R: decoded, N: rawSize + 1}
	if err := extractSelected(bounded, policy.installation.resolve, rawSize, policy); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, bounded); err != nil {
		return err
	}
	if bounded.N != 1 {
		return errors.New("decompressed archive does not match its declared size")
	}
	return nil
}

// preserveSourceArchive finalizes the original archive and manifest inside its private preservation slot.
func preserveSourceArchive(archive *os.File, hello message) error {
	manifest, err := json.MarshalIndent(hello, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(archive.Name())
	// #nosec G304 -- directory is the receiver-owned private preservation slot.
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifest, 0600); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	name := "source.tar"
	if hello.Encoding == "gzip" {
		name += ".gz"
	}
	return os.Rename(archive.Name(), filepath.Join(directory, name))
}
