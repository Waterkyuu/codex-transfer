package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// migration implements local inspection and the two terminal transfer roles.
func (a *application) migration(ctx context.Context, command string, args []string) error {
	options, positional, err := a.parseOptions(command, args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	var address string
	if command == "send" {
		ip := strings.Trim(positional[0], "[]")
		if net.ParseIP(ip) == nil {
			return errors.New("send requires a literal IPv4 or IPv6 address")
		}
		address = net.JoinHostPort(ip, strconv.Itoa(options.port))
	}
	if command == "receive" && net.ParseIP(options.listen) == nil {
		return errors.New("listen requires a literal IPv4 or IPv6 address")
	}
	source := command != "receive" && !options.all
	roots, err := options.roots(source, command == "receive")
	if err != nil {
		return err
	}
	if command == "paths" {
		a.printRoots(roots)
		return nil
	}
	guard := migrate.CheckStopped
	if options.skipCheck {
		_, _ = fmt.Fprintln(
			a.output,
			"Process check disabled. Use only synthetic data; live databases may be inconsistent.",
		)
		guard = nil
	}
	if guard != nil {
		if err := guard(); err != nil {
			return err
		}
	}
	if command == "check" {
		a.printRoots(roots)
		if len(roots) == 0 {
			return errors.New("no Codex source data found")
		}
		_, _ = fmt.Fprintln(
			a.output,
			"Local startup check passed. Both transfer terminals must also connect and approve pairing.",
		)
		return nil
	}
	a.printRoots(roots)
	if command == "send" {
		sender := &migrate.Sender{
			Roots:       roots,
			Output:      a.output,
			Fingerprint: options.fingerprint,
			Confirm:     a.confirm,
			Check:       guard,
			Wait:        options.wait,
			Workers:     options.workers,
			Compression: options.compression,
		}
		return sender.Send(ctx, address)
	}
	targets := make(map[string]string, len(roots))
	for _, root := range roots {
		targets[root.Name] = root.Path
	}
	overrides := map[string]string{}
	for _, mapping := range options.mappings {
		name, target, _ := strings.Cut(mapping, "=")
		overrides[name] = target
	}
	pathMappings := map[string]string{}
	for _, mapping := range options.pathMappings {
		source, target, _ := strings.Cut(mapping, "=")
		if _, exists := pathMappings[source]; exists {
			return fmt.Errorf("duplicate source path mapping %q", source)
		}
		pathMappings[source] = target
	}
	receiver := &migrate.Receiver{
		Targets: targets, Output: a.output, MaxBytes: options.maxGiB * (1 << 30),
		Confirm: a.confirm, Check: guard, Overrides: overrides, PathMappings: pathMappings,
		ImportRoot: options.importRoot, Workers: options.workers,
	}
	if err := receiver.Prepare(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(options.listen, strconv.Itoa(options.port)))
	if err != nil {
		return fmt.Errorf("start receiver: %w", err)
	}
	defer func() { _ = listener.Close() }()
	_, _ = fmt.Fprintln(
		a.output,
		"Destination roots offered by the sender will be replaced; existing data will be backed up.",
	)
	return receiver.Serve(ctx, listener)
}

// printRoots shows locations and availability without exposing credential or conversation contents.
func (a *application) printRoots(roots []migrate.Root) {
	for _, root := range roots {
		status := "exists"
		if _, err := os.Lstat(root.Path); errors.Is(err, os.ErrNotExist) {
			status = "missing"
		}
		_, _ = fmt.Fprintf(a.output, "%s\t%s\t[%s]\n", root.Name, root.Path, status)
	}
}
