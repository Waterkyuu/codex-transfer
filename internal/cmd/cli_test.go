package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestVersionCommand exposes release metadata without inspecting local Codex state.
func TestVersionCommand(t *testing.T) {
	for _, command := range []string{"version", "--version"} {
		var output bytes.Buffer
		options := Options{Input: strings.NewReader(""), Output: &output, Version: "development"}
		if err := Run(context.Background(), []string{command}, options); err != nil {
			t.Fatal(err)
		}
		if output.String() != "codex-transfer development\n" {
			t.Fatalf("version: %q", output.String())
		}
	}
}

// TestCLIUsage verifies the discoverable command surface and rejects unknown commands.
func TestCLIUsage(t *testing.T) {
	var output bytes.Buffer
	app := &application{input: strings.NewReader(""), output: &output, version: "development"}
	if err := app.run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"address", "paths", "check", "receive", "send"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("missing %s in help", command)
		}
	}
	if err := app.run(context.Background(), []string{"unknown"}); err == nil {
		t.Fatal("unknown command accepted")
	}
}

// TestPairingRequiresExplicitApproval rejects absent input and a declined pairing code.
func TestPairingRequiresExplicitApproval(t *testing.T) {
	for _, answer := range []string{"", "no\n", "yes\n"} {
		var output bytes.Buffer
		app := &application{input: strings.NewReader(answer), output: &output}
		if err := app.confirm(context.Background(), "123456abcdef"); err == nil {
			t.Fatal("pairing accepted without exact code")
		}
	}
	var output bytes.Buffer
	app := &application{input: strings.NewReader("123456abcdef\n"), output: &output}
	if err := app.confirm(context.Background(), "123456abcdef"); err != nil {
		t.Fatal(err)
	}
}

// TestSendValidation rejects DNS names and invalid ports before touching source data.
func TestSendValidation(t *testing.T) {
	for _, args := range [][]string{
		{"send", "example.com"},
		{"send", "127.0.0.1", "--port", "0"},
		{"send", "127.0.0.1", "--wait", "-1s"},
		{"send", "127.0.0.1", "--workers", "-1"},
		{"send", "127.0.0.1", "--compression", "unsupported"},
		{"send", "127.0.0.1", "--since", "invalid"},
		{"send", "127.0.0.1", "--until", "2026-02-30"},
		{"send", "127.0.0.1", "--since", "2026-10-03", "--until", "2026-10-02"},
		{"send", "127.0.0.1", "--project-prefix", " "},
		{"send", "127.0.0.1", "--project-contains", ""},
		{"send", "127.0.0.1", "--project-prefix", "app", "--extra", "data=/tmp/data"},
		{"receive", "--project-prefix", "app"},
		{"receive", "--workers", "257"},
		{"receive", "--archive-dir", "relative"},
	} {
		var output bytes.Buffer
		app := &application{input: strings.NewReader(""), output: &output, version: "development"}
		if err := app.run(context.Background(), args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}

// TestSendSessionRules exposes repeatable selection flags and keeps the default full migration unchanged.
func TestSendSessionRules(t *testing.T) {
	var output bytes.Buffer
	app := &application{output: &output}
	values, _, err := app.parseOptions("send", []string{"127.0.0.1", "--since", "2026-10-01", "--until", "2026-10-02",
		"--project-prefix", "app-", "--project-prefix", "web-", "--project-contains", "api"})
	if err != nil || values.filter == nil {
		t.Fatalf("session rules not parsed: %v", err)
	}
	values, _, err = app.parseOptions("send", []string{"127.0.0.1"})
	if err != nil || values.filter != nil {
		t.Fatalf("default full migration changed: %v", err)
	}
}
