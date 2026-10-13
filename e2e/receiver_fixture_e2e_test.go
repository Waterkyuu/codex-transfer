package e2e_test

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// receiverPeer owns a CLI receiver and an independently authenticated test protocol connection.
type receiverPeer struct {
	// connection carries the scenario's explicit offers and archive bytes.
	connection *tls.Conn
	// command is reaped on completion or killed and reaped during failed-test cleanup.
	command *exec.Cmd
	// output yields the complete subprocess log once its output stream closes.
	output <-chan string
	// log preserves diagnostics after wait consumes output.
	log string
}

// startReceiverPeer adds a loopback endpoint to the scenario command and approves its pinned TLS peer.
func startReceiverPeer(t *testing.T, command *exec.Cmd) *receiverPeer {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(reserved.Addr().(*net.TCPAddr).Port)
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	command.Args = append(
		command.Args,
		"--listen",
		"127.0.0.1",
		"--port",
		port,
	)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	lines, output, err := startCaptured(command)
	if err != nil {
		t.Fatal(err)
	}
	peer := &receiverPeer{command: command, output: output}
	// Register ownership before authentication so any failed setup still releases and reaps the process.
	t.Cleanup(func() {
		if peer.connection != nil {
			_ = peer.connection.Close()
		}
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			peer.log = <-output
			_ = command.Wait()
		}
		if t.Failed() {
			t.Logf("receiver output:\n%s", peer.log)
		}
	})
	waitForLine(t, lines, "Receiver listening at ")
	fingerprint := strings.TrimPrefix(waitForLine(t, lines, "Certificate SHA-256: "), "Certificate SHA-256: ")
	connection, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp",
		net.JoinHostPort("127.0.0.1", port),
		&tls.Config{
			MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
			VerifyConnection: func(state tls.ConnectionState) error {
				digest := sha256.Sum256(state.PeerCertificates[0].Raw)
				if hex.EncodeToString(digest[:]) != fingerprint {
					return fmt.Errorf("receiver certificate does not match local output")
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	peer.connection = connection
	if err := connection.SetDeadline(time.Now().Add(45 * time.Second)); err != nil {
		t.Fatal(err)
	}
	state := connection.ConnectionState()
	code, err := state.ExportKeyingMaterial("codex-transfer-pairing-v1", nil, 6)
	if err != nil {
		t.Fatal(err)
	}
	if actual := waitForLine(t, lines, "Pairing code: "); actual != "Pairing code: "+hex.EncodeToString(code) {
		t.Fatal("TLS pairing mismatch")
	}
	if _, err := io.WriteString(input, hex.EncodeToString(code)+"\n"); err != nil {
		t.Fatal(err)
	}
	return peer
}

// wait drains receiver diagnostics and verifies successful CLI completion.
func (peer *receiverPeer) wait(t *testing.T) {
	t.Helper()
	peer.log = <-peer.output
	if err := peer.command.Wait(); err != nil {
		t.Fatalf("receiver: %v\n%s", err, peer.log)
	}
}
