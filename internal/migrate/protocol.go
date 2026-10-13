package migrate

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

const (
	// protocolVersion rejects incompatible framing and installation semantics.
	protocolVersion = 2
	// maxFrame bounds memory allocated for an untrusted control message.
	maxFrame = 1 << 20
	// idleTimeout bounds every network read and write, while allowing long transfers.
	idleTimeout = 60 * time.Second
)

// message describes readiness, archive metadata, or a final installation acknowledgement.
type message struct {
	// Version identifies the transfer protocol rather than the installed Codex version.
	Version int `json:"version"`
	// Kind names the expected handshake or transfer phase.
	Kind string `json:"kind"`
	// OS selects platform-specific source interpretation without controlling receiver destinations.
	OS string `json:"os,omitempty"`
	// Home supplies a source prefix used only for structured path translation.
	Home string `json:"home,omitempty"`
	// Paths maps root names to source prefixes used only for structured path translation.
	Paths map[string]string `json:"paths,omitempty"`
	// StatePaths resolves top-level config paths relative to the sender's working directory.
	StatePaths map[string]string `json:"state_paths,omitempty"`
	// Encoding selects bounded gzip members or an uncompressed tar stream.
	Encoding string `json:"encoding,omitempty"`
	// RawSize independently bounds the decompressed tar stream.
	RawSize int64 `json:"raw_size,omitempty"`
	// Roots lists portable root identifiers without exposing source absolute paths.
	Roots []string `json:"roots,omitempty"`
	// Size bounds the exact archive payload on the connection.
	Size int64 `json:"size,omitempty"`
	// SHA256 provides an end-to-end integrity check before destination replacement.
	SHA256 string `json:"sha256,omitempty"`
	// Error explains a remote refusal or installation failure.
	Error string `json:"error,omitempty"`
}

// ephemeralCertificate creates a fresh TLS identity for each receiver invocation.
func ephemeralCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "codex-transfer"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	hash := sha256.Sum256(der)
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return certificate, hex.EncodeToString(hash[:]), nil
}

// pairingCode binds human verification to this TLS session, including both peers' key agreement.
func pairingCode(connection *tls.Conn) (string, error) {
	state := connection.ConnectionState()
	data, err := state.ExportKeyingMaterial("codex-transfer-pairing-v1", nil, 6)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// writeMessage emits a length-delimited JSON frame with a bounded write deadline.
func writeMessage(connection net.Conn, value message) error {
	value.Version = protocolVersion
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return errors.New("control message too large")
	}
	if err := connection.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
		return err
	}
	var header [4]byte
	// #nosec G115 -- len(data) is bounded to maxFrame (1 MiB) before this conversion.
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := io.Copy(connection, bytes.NewReader(header[:])); err != nil {
		return err
	}
	_, err = io.Copy(connection, bytes.NewReader(data))
	return err
}

// readMessage validates frame size, protocol version, and the remote error channel.
func readMessage(connection net.Conn) (message, error) {
	if err := connection.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
		return message{}, err
	}
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return message{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > maxFrame {
		return message{}, errors.New("invalid control frame length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(connection, data); err != nil {
		return message{}, err
	}
	var value message
	if err := json.Unmarshal(data, &value); err != nil {
		return value, err
	}
	if value.Version != protocolVersion {
		return value, errors.New("incompatible transfer protocol")
	}
	if value.Error != "" {
		return value, fmt.Errorf("peer: %s", value.Error)
	}
	return value, nil
}

// networkReader refreshes the idle deadline for every streaming read.
type networkReader struct {
	// connection is owned and closed by the current transfer operation.
	connection net.Conn
}

// Read bounds network inactivity without limiting the total transfer duration.
func (r networkReader) Read(data []byte) (int, error) {
	if err := r.connection.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
		return 0, err
	}
	return r.connection.Read(data)
}

// networkWriter refreshes the idle deadline for every streaming write.
type networkWriter struct {
	// connection is owned and closed by the current transfer operation.
	connection net.Conn
}

// Write bounds network inactivity without limiting the total transfer duration.
func (w networkWriter) Write(data []byte) (int, error) {
	if err := w.connection.SetWriteDeadline(time.Now().Add(idleTimeout)); err != nil {
		return 0, err
	}
	return w.connection.Write(data)
}
