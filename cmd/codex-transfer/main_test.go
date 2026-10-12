package main

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
		app := &application{input: strings.NewReader(""), output: &output}
		if err := app.run(context.Background(), []string{command}); err != nil {
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
	app := &application{input: strings.NewReader(""), output: &output}
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
	} {
		var output bytes.Buffer
		app := &application{input: strings.NewReader(""), output: &output}
		if err := app.run(context.Background(), args); err == nil {
			t.Fatalf("invalid command accepted: %v", args)
		}
	}
}
