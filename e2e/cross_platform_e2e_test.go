package e2e_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// writePeerMessage exercises the published protocol independently of the receiver implementation.
func writePeerMessage(t *testing.T, connection net.Conn, value map[string]any) {
	t.Helper()
	value["version"] = 2
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := io.Copy(connection, bytes.NewReader(append(header[:], data...))); err != nil {
		t.Fatal(err)
	}
}

// readPeerMessage verifies framed readiness and acknowledgement from a real executable.
func readPeerMessage(t *testing.T, connection net.Conn, expected string) {
	t.Helper()
	for {
		var header [4]byte
		if _, err := io.ReadFull(connection, header[:]); err != nil {
			t.Fatal(err)
		}
		length := binary.BigEndian.Uint32(header[:])
		if length == 0 || length > 1<<20 {
			t.Fatal("invalid control frame")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(connection, data); err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		if expected == "complete" && message["kind"] == "working" {
			continue
		}
		if message["kind"] != expected || message["error"] != nil {
			t.Fatalf("peer phase %s: %s", expected, data)
		}
		return
	}
}

// crossPlatformFixture builds representative config, rollout, SQLite, workspace, and foreign desktop state.
func crossPlatformFixture(t *testing.T, sourceHome string) []byte {
	t.Helper()
	canonical := strings.ReplaceAll(sourceHome, `\`, "/")
	state := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite3", state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"CREATE TABLE threads (id TEXT PRIMARY KEY, cwd TEXT, rollout_path TEXT, title TEXT)",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO threads VALUES (?, ?, ?, ?)",
		"thread-one",
		canonical+"/Documents/Codex/project",
		canonical+"/.codex/sessions/one.jsonl",
		canonical+"/unchanged-title",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(
		"# original comment\nsqlite_home = '%s/external-state'\n[projects.\"%s/Documents/Codex/project\"]\ntrust_level = 'trusted'\n",
		canonical,
		canonical,
	)
	meta, err := json.Marshal(
		map[string]any{
			"type":    "session_meta",
			"payload": map[string]any{"cwd": canonical + "/Documents/Codex/project"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := json.Marshal(
		map[string]any{"type": "response_item", "payload": map[string]any{"text": canonical + "/unchanged-prompt"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"codex/config.toml":          []byte(config),
		"codex/sessions/one.jsonl":   append(append(meta, '\n'), append(prompt, '\n')...),
		"sqlite/state.sqlite":        database,
		"workspace/project/note.txt": []byte("portable workspace"),
		"desktop/platform-settings":  []byte("foreign desktop state"),
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, directory := range []string{"codex", "codex/sessions", "sqlite", "workspace", "workspace/project", "desktop"} {
		if err := writer.WriteHeader(&tar.Header{Name: directory, Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		if err := writer.WriteHeader(
			&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// compressPeerFixture builds standard gzip with the published optional member hints.
func compressPeerFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	writer.Extra = make([]byte, 12)
	copy(writer.Extra, "CTF1")
	binary.LittleEndian.PutUint32(writer.Extra[4:8], uint32(len(data)))
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := output.Bytes()
	binary.LittleEndian.PutUint32(encoded[20:24], uint32(len(encoded)))
	return encoded
}

// nativeDesktop locates the fixture's platform-specific active settings directory.
func nativeDesktop(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Codex")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Codex")
	default:
		return filepath.Join(home, ".config", "Codex")
	}
}

// TestCLICrossPlatformImport tests both foreign source OSes against the real host receiver on every CI platform.
func TestCLICrossPlatformImport(t *testing.T) {
	binary := buildCLI(t)
	for _, sourceOS := range []string{"darwin", "linux", "windows"} {
		if sourceOS == runtime.GOOS {
			continue
		}
		t.Run(sourceOS+"_to_"+runtime.GOOS, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			home := t.TempDir()
			if err := os.MkdirAll(nativeDesktop(home), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(nativeDesktop(home), "keep.txt"),
				[]byte("native settings"),
				0600,
			); err != nil {
				t.Fatal(err)
			}
			reserved, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := reserved.Addr().(*net.TCPAddr).Port
			if err := reserved.Close(); err != nil {
				t.Fatal(err)
			}
			receiver := exec.CommandContext(
				ctx,
				binary,
				"receive",
				"--listen",
				"127.0.0.1",
				"--port",
				fmt.Sprint(port),
				"--workers",
				"2",
				"--skip-process-check",
			)
			receiver.Env = fixtureEnvironment(home)
			input, err := receiver.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			lines, output, err := startCaptured(receiver)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if receiver.ProcessState == nil {
					_ = receiver.Process.Kill()
					_ = receiver.Wait()
				}
			})
			waitForLine(t, lines, "Receiver listening at ")
			fingerprint := strings.TrimPrefix(waitForLine(t, lines, "Certificate SHA-256: "), "Certificate SHA-256: ")
			connection, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), &tls.Config{
				MinVersion: tls.VersionTLS13, InsecureSkipVerify: true,
				VerifyConnection: func(state tls.ConnectionState) error {
					hash := sha256.Sum256(state.PeerCertificates[0].Raw)
					if hex.EncodeToString(hash[:]) != fingerprint {
						return fmt.Errorf("receiver certificate does not match local output")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
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
			sourceHome := "/home/source"
			switch sourceOS {
			case "darwin":
				sourceHome = "/Users/source"
			case "windows":
				sourceHome = `C:\Users\source`
			}
			archive := crossPlatformFixture(t, sourceHome)
			encoded, encoding := archive, "none"
			if sourceOS == "linux" {
				encoded, encoding = compressPeerFixture(t, archive), "gzip"
			}
			hash := sha256.Sum256(encoded)
			canonical := strings.ReplaceAll(sourceHome, `\`, "/")
			writePeerMessage(t, connection, map[string]any{
				"kind":  "offer",
				"os":    sourceOS,
				"home":  sourceHome,
				"roots": []string{"codex", "sqlite", "workspace", "desktop"},
				"paths": map[string]string{
					"codex":     canonical + "/.codex",
					"sqlite":    canonical + "/external-state",
					"workspace": canonical + "/Documents/Codex",
					"desktop":   canonical + "/desktop",
				},
				"size":     len(encoded),
				"encoding": encoding,
				"raw_size": len(archive),
				"sha256":   hex.EncodeToString(hash[:]),
			})
			readPeerMessage(t, connection, "ready")
			if _, err := io.Copy(connection, bytes.NewReader(encoded)); err != nil {
				t.Fatal(err)
			}
			readPeerMessage(t, connection, "complete")
			log := <-output
			if err := receiver.Wait(); err != nil {
				t.Fatalf("receiver: %v\n%s", err, log)
			}
			assertCrossPlatformResult(t, home, sourceOS, canonical, encoded, encoding)
		})
	}
}

// assertCrossPlatformResult verifies adapted SQLite paths, preserved message text, and complete source bytes.
func assertCrossPlatformResult(t *testing.T, home, sourceOS, canonical string, archive []byte, encoding string) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(home, ".codex.sqlite", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cwd, rollout, title string
	if err := db.QueryRow("SELECT cwd, rollout_path, title FROM threads WHERE id = ?", "thread-one").
		Scan(&cwd, &rollout, &title); err != nil {
		t.Fatal(err)
	}
	if cwd != filepath.Join(home, "Documents", "Codex", "project") ||
		rollout != filepath.Join(home, ".codex", "sessions", "one.jsonl") ||
		title != canonical+"/unchanged-title" {
		t.Fatalf("SQLite translation: %q, %q, %q", cwd, rollout, title)
	}
	config, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(config), filepath.ToSlash(filepath.Join(home, ".codex.sqlite"))) {
		t.Fatalf("config: %s, %v", config, err)
	}
	rolloutData, err := os.ReadFile(rollout)
	if err != nil || !strings.Contains(string(rolloutData), canonical+"/unchanged-prompt") {
		t.Fatalf("rollout content: %s, %v", rolloutData, err)
	}
	name := "source.tar"
	if encoding == "gzip" {
		name += ".gz"
	}
	saved, err := os.ReadFile(filepath.Join(home, ".codex-transfer", "imports", sourceOS, name))
	if err != nil || !bytes.Equal(saved, archive) {
		t.Fatalf("original source archive: %v", err)
	}
	workspace, err := os.ReadFile(filepath.Join(home, "Documents", "Codex", "project", "note.txt"))
	if err != nil || string(workspace) != "portable workspace" {
		t.Fatalf("workspace: %q, %v", workspace, err)
	}
	native, err := os.ReadFile(filepath.Join(nativeDesktop(home), "keep.txt"))
	if err != nil || string(native) != "native settings" {
		t.Fatal("foreign state replaced native desktop settings")
	}
	if _, err := os.Stat(filepath.Join(nativeDesktop(home), "platform-settings")); !os.IsNotExist(err) {
		t.Fatal("foreign desktop state was activated")
	}
}
