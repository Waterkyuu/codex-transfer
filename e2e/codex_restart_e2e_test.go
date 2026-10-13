package e2e_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// codexRPCMessage contains only the protocol fields needed for real-process restart verification.
type codexRPCMessage struct {
	// ID associates a response with the client's request; notifications use zero.
	ID int `json:"id"`
	// Method identifies notifications emitted by the real app-server.
	Method string `json:"method"`
	// Result retains response data without duplicating the full upstream schema.
	Result json.RawMessage `json:"result"`
	// Params retains notification data, including completed turn status.
	Params json.RawMessage `json:"params"`
	// Error preserves server diagnostics when an RPC request fails.
	Error json.RawMessage `json:"error"`
}

// codexTestProcess owns one isolated real app-server and its bounded JSON-RPC connection.
type codexTestProcess struct {
	// input encodes requests into the process's private stdin pipe.
	input *json.Encoder
	// stdin closes the connection before waiting for graceful shutdown.
	stdin io.WriteCloser
	// messages receives decoded stdout messages until the process exits.
	messages <-chan codexRPCMessage
	// completed retains early completion events observed while awaiting RPC responses.
	completed []json.RawMessage
	// done reports process exit without blocking protocol reads.
	done <-chan error
	// cancel terminates only this test's process on failure or timeout.
	cancel context.CancelFunc
	// nextID supplies unique request IDs within this connection.
	nextID int
	// log stores isolated diagnostic output for failures.
	log *os.File
}

// isolatedCodexEnvironment excludes credentials, proxies, and the developer's configuration from child processes.
func isolatedCodexEnvironment(t *testing.T, home string) []string {
	t.Helper()
	for _, directory := range []string{"tmp", ".config", ".cache", ".local/share"} {
		if err := os.MkdirAll(filepath.Join(home, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"USERPROFILE=" + home,
		"CODEX_HOME=" + filepath.Join(home, ".codex"),
		"CODEX_SQLITE_HOME=" + filepath.Join(home, ".codex", "sqlite"),
		"TMPDIR=" + filepath.Join(home, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"LANG=en_US.UTF-8",
	}
}

// prepareRealCodexHome creates test-owned paths and a local-only provider without copying any user files.
func prepareRealCodexHome(t *testing.T, providerURL string) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{".codex/sqlite", "app-api"} {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(directory)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := fmt.Sprintf(`model = "fixture-model"
model_provider = "fixture"
model_context_window = 8192
cli_auth_credentials_store = "file"
[model_providers.fixture]
name = "Isolated fixture"
base_url = %q
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
[analytics]
enabled = false
[feedback]
enabled = false
`, providerURL+"/v1")
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

// startRealCodex launches an independent app-server and verifies that its resolved home belongs to the test.
func startRealCodex(t *testing.T, binary, home string) *codexTestProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	command := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	command.Env = isolatedCodexEnvironment(t, home)
	command.Dir = filepath.Join(home, "app-api")
	log, err := os.CreateTemp(home, "app-server-*.log")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stderr = log
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	messages, done := make(chan codexRPCMessage, 128), make(chan error, 1)
	process := &codexTestProcess{
		input: json.NewEncoder(stdin), stdin: stdin, messages: messages,
		completed: []json.RawMessage{}, done: done, cancel: cancel, log: log,
	}
	t.Cleanup(func() {
		cancel()
		_ = log.Close()
	})
	go func() {
		defer close(messages)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 16<<20)
		for scanner.Scan() {
			var message codexRPCMessage
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { done <- command.Wait() }()
	initialized := process.call(t, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "codex_transfer_isolation_test", "version": "1.0.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	})
	var resolved struct {
		// CodexHome confirms the server did not select the developer's persistent state.
		CodexHome string `json:"codexHome"`
	}
	if err := json.Unmarshal(
		initialized,
		&resolved,
	); err != nil ||
		resolved.CodexHome != filepath.Join(home, ".codex") {
		t.Fatalf("unexpected real Codex home: %s, %v", resolved.CodexHome, err)
	}
	if err := process.input.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	return process
}

// next reads one protocol message with a bounded wait and isolated failure diagnostics.
func (p *codexTestProcess) next(t *testing.T) codexRPCMessage {
	t.Helper()
	select {
	case message, ok := <-p.messages:
		if ok {
			return message
		}
	case <-time.After(30 * time.Second):
	}
	p.cancel()
	data, _ := os.ReadFile(p.log.Name())
	t.Fatalf("real Codex stopped or timed out: %s", data)
	return codexRPCMessage{}
}

// call sends one RPC request and preserves completion events that arrive before its response.
func (p *codexTestProcess) call(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	p.nextID++
	if err := p.input.Encode(map[string]any{"id": p.nextID, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	for {
		message := p.next(t)
		if message.Method == "turn/completed" {
			p.completed = append(p.completed, message.Params)
		}
		if message.ID != p.nextID {
			continue
		}
		if len(message.Error) > 0 {
			t.Fatalf("real Codex %s failed: %s", method, message.Error)
		}
		return message.Result
	}
}

// turn verifies that the real Codex turn completes successfully using the local Responses fixture.
func (p *codexTestProcess) turn(t *testing.T, threadID, text string) {
	t.Helper()
	p.call(t, "turn/start", map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": text}},
	})
	for len(p.completed) == 0 {
		message := p.next(t)
		if message.Method == "turn/completed" {
			p.completed = append(p.completed, message.Params)
		}
	}
	var completed struct {
		// ThreadID ensures completion belongs to the resumed conversation.
		ThreadID string `json:"threadId"`
		// Turn supplies the native lifecycle status rather than inferring success from output text.
		Turn struct {
			// Status must be completed; failed or interrupted generations are not successful resumptions.
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(p.completed[0], &completed); err != nil || completed.ThreadID != threadID ||
		completed.Turn.Status != "completed" {
		t.Fatalf("real Codex turn did not complete: %s, %v", p.completed[0], err)
	}
	p.completed = p.completed[1:]
}

// stop closes one test-owned process and waits for its SQLite writers to exit before migration or restart.
func (p *codexTestProcess) stop(t *testing.T) {
	t.Helper()
	_ = p.stdin.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("real Codex shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		p.cancel()
		t.Fatal("real Codex did not stop before migration")
	}
	p.cancel()
}

// createRealCodexThread records a real legacy rollout instead of constructing a synthetic database schema.
func createRealCodexThread(t *testing.T, process *codexTestProcess, home, marker string) string {
	t.Helper()
	result := process.call(t, "thread/start", map[string]any{
		"cwd": filepath.Join(home, "app-api"), "model": "fixture-model", "modelProvider": "fixture",
		"approvalPolicy": "never", "sandbox": "read-only", "historyMode": "legacy",
	})
	var created struct {
		// Thread contains the native identity and storage mode returned by Codex.
		Thread struct {
			// ID is used unchanged for migration and subsequent resumption.
			ID string `json:"id"`
			// HistoryMode confirms conversation content is self-contained in the rollout.
			HistoryMode string `json:"historyMode"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &created); err != nil || created.Thread.ID == "" ||
		created.Thread.HistoryMode != "legacy" {
		t.Fatalf("real Codex did not create a legacy thread: %v", err)
	}
	process.turn(t, created.Thread.ID, marker)
	return created.Thread.ID
}

// localResponsesFixture captures model-visible history and returns deterministic responses without external services.
func localResponsesFixture(t *testing.T) (string, <-chan string) {
	t.Helper()
	requests := make(chan string, 32)
	var sequence atomic.Uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected fixture request", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("isolated Codex sent unexpected credentials to the fixture")
			http.Error(w, "unexpected credentials", http.StatusForbidden)
			return
		}
		id := sequence.Add(1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{
			"id": fmt.Sprintf("fixture-message-%d", id), "type": "message", "role": "assistant", "status": "completed",
			"phase": "final_answer", "content": []map[string]any{{
				"type": "output_text", "text": "ISOLATED_MODEL_REPLY", "annotations": []string{},
			}},
		}
		for _, event := range []map[string]any{
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{
				"id": fmt.Sprintf("fixture-response-%d", id), "status": "completed", "output": []map[string]any{item},
				"usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25,
					"input_tokens_details": map[string]int{"cached_tokens": 0}},
			}},
		} {
			data, err := json.Marshal(event)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, requests
}

// assertRealCodexHistory confirms native history hydration includes each expected message after a process restart.
func assertRealCodexHistory(t *testing.T, process *codexTestProcess, threadID string, markers ...string) {
	t.Helper()
	result := process.call(t, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	for _, marker := range markers {
		if !strings.Contains(string(result), marker) {
			t.Fatalf("real Codex history for %s omitted %q", threadID, marker)
		}
	}
}

// TestRealCodexSessionRestart verifies native discovery, history hydration, continuation, and persistence in isolated homes.
func TestRealCodexSessionRestart(t *testing.T) {
	codexBinary := os.Getenv("CODEX_TRANSFER_CODEX_BINARY")
	if codexBinary == "" {
		t.Skip("set CODEX_TRANSFER_CODEX_BINARY to an absolute real Codex executable for isolated restart verification")
	}
	if !filepath.IsAbs(codexBinary) {
		t.Fatal("CODEX_TRANSFER_CODEX_BINARY must be absolute")
	}
	binary := buildCLI(t)
	for _, initialized := range []bool{true, false} {
		t.Run(fmt.Sprintf("initialized_receiver_%t", initialized), func(t *testing.T) {
			providerURL, requests := localResponsesFixture(t)
			source, destination := prepareRealCodexHome(t, providerURL), prepareRealCodexHome(t, providerURL)
			version := exec.Command(codexBinary, "--version")
			version.Env = isolatedCodexEnvironment(t, source)
			version.Dir = filepath.Join(source, "app-api")
			output, err := version.CombinedOutput()
			if err != nil {
				t.Fatalf("real Codex version: %s, %v", output, err)
			}
			t.Log(strings.TrimSpace(string(output)))
			process := startRealCodex(t, codexBinary, source)
			active := createRealCodexThread(t, process, source, "SOURCE_ACTIVE_CANARY")
			archived := createRealCodexThread(t, process, source, "SOURCE_ARCHIVED_CANARY")
			process.call(t, "thread/archive", map[string]any{"threadId": archived})
			process.stop(t)
			local := ""
			if initialized {
				process = startRealCodex(t, codexBinary, destination)
				local = createRealCodexThread(t, process, destination, "RECEIVER_LOCAL_CANARY")
				process.call(t, "thread/name/set", map[string]any{"threadId": local, "name": "Retained local name"})
				process.stop(t)
				// Pinning is deliberately SQLite-only so replaying rollout metadata cannot satisfy preservation.
				db, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "sqlite", "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE threads SET is_pinned=1 WHERE id=?", local); err != nil {
					_ = db.Close()
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			// Both native processes have exited before the migration CLI uses its synthetic-data process-check bypass.
			receiverMode := "environment"
			if initialized {
				receiverMode = "cli"
			}
			runFilteredTransfer(t, binary, source, destination, "environment", receiverMode, "", "")
			process = startRealCodex(t, codexBinary, destination)
			for _, selection := range []struct {
				// id is the native identity that must be discoverable after importing its rollout.
				id string
				// archived selects the corresponding native conversation history list.
				archived bool
			}{
				{id: active}, {id: archived, archived: true},
			} {
				listed := process.call(t, "thread/list", map[string]any{
					"limit": 100, "modelProviders": []string{}, "archived": selection.archived,
					"useStateDbOnly": true,
				})
				if !strings.Contains(string(listed), selection.id) {
					t.Fatalf(
						"real Codex did not list imported thread %s (archived=%t): %s",
						selection.id,
						selection.archived,
						listed,
					)
				}
			}
			if local != "" {
				assertRealCodexHistory(t, process, local, "RECEIVER_LOCAL_CANARY", "Retained local name")
			}
			for _, thread := range []struct {
				// id selects an imported thread for real resumption.
				id string
				// marker proves its earlier conversation reaches the resumed model request.
				marker string
			}{
				{id: active, marker: "SOURCE_ACTIVE_CANARY"},
				{id: archived, marker: "SOURCE_ARCHIVED_CANARY"},
			} {
				assertRealCodexHistory(t, process, thread.id, thread.marker, "ISOLATED_MODEL_REPLY")
				if thread.id == archived {
					process.call(t, "thread/unarchive", map[string]any{"threadId": thread.id})
				}
				process.call(t, "thread/resume", map[string]any{"threadId": thread.id})
				process.turn(t, thread.id, "CONTINUED_AFTER_IMPORT")
				found := false
				for len(requests) > 0 {
					request := <-requests
					found = found ||
						strings.Contains(request, thread.marker) && strings.Contains(request, "CONTINUED_AFTER_IMPORT") &&
							strings.Contains(request, "ISOLATED_MODEL_REPLY")
				}
				if !found {
					t.Fatal("resumed model request omitted the imported conversation")
				}
			}
			process.stop(t)
			process = startRealCodex(t, codexBinary, destination)
			assertRealCodexHistory(t, process, active, "SOURCE_ACTIVE_CANARY", "CONTINUED_AFTER_IMPORT")
			assertRealCodexHistory(t, process, archived, "SOURCE_ARCHIVED_CANARY", "CONTINUED_AFTER_IMPORT")
			if local != "" {
				assertRealCodexHistory(t, process, local, "RECEIVER_LOCAL_CANARY", "Retained local name")
			}
			process.stop(t)
			if local != "" {
				db, err := sql.Open("sqlite3", filepath.Join(destination, ".codex", "sqlite", "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var pinned int
				if err := db.QueryRow("SELECT is_pinned FROM threads WHERE id=?", local).
					Scan(&pinned); err != nil ||
					pinned != 1 {
					t.Fatalf("SQLite-only receiving pin changed: %d, %v", pinned, err)
				}
			}
			t.Log("native lists, imported histories, completed resumed turns, and second-restart persistence verified")
		})
	}
}
