package cmd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/waterkyuu/codex-transfer/internal/migrate"
)

// pathFlags collects repeated named source additions or receiver destination overrides.
type pathFlags []string

// String exposes flag values in usage without reading file contents.
func (p *pathFlags) String() string { return strings.Join(*p, ", ") }

// Set validates portable named-root syntax before adding a repeated argument.
func (p *pathFlags) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || name == "" || path == "" {
		return errors.New("expected NAME=/absolute/path")
	}
	*p = append(*p, value)
	return nil
}

// options holds one command's explicit data paths, network limits, and process-check policy.
type options struct {
	// codexHome selects the main Codex state root.
	codexHome string
	// sqliteHome optionally selects separately stored SQLite state.
	sqliteHome string
	// port selects the listening port shared by both computers.
	port int
	// listen selects a literal local IP address on the receiving computer.
	listen string
	// wait bounds source connection retries without limiting stream duration.
	wait time.Duration
	// maxGiB bounds receiver archive size in binary gibibytes.
	maxGiB int64
	// fingerprint optionally pins the receiving certificate in addition to human pairing.
	fingerprint string
	// extra lists additional source data roots owned by Codex.
	extra pathFlags
	// mappings assigns local receiving destinations to named source roots.
	mappings pathFlags
	// skipCheck permits synthetic fixture migration while this development session runs.
	skipCheck bool
	// all includes missing conventional locations in path output.
	all bool
	// workers sets parallel CPU processing; zero uses the runtime CPU budget.
	workers int
	// compression selects sender CPU effort or disables compression for a fast local network.
	compression string
	// importRoot selects where unchanged foreign snapshots are preserved.
	importRoot string
	// pathMappings overrides source project prefixes independently of root installation paths.
	pathMappings pathFlags
	// filter selects session merging instead of full-root replacement.
	filter *migrate.SessionFilter
}

// parseOptions supports the documented IP-first send syntax and rejects unused arguments.
func (a *application) parseOptions(command string, args []string) (*options, []string, error) {
	values := &options{extra: pathFlags{}, mappings: pathFlags{}, pathMappings: pathFlags{}}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var since, until string
	prefixes, contains := []string{}, []string{}
	flags.SetOutput(a.output)
	flags.StringVar(&values.codexHome, "codex-home", os.Getenv("CODEX_HOME"), "Codex state root (default ~/.codex)")
	flags.StringVar(&values.sqliteHome, "sqlite-home", "", "Override separately stored SQLite root")
	flags.BoolVar(
		&values.skipCheck,
		"skip-process-check",
		false,
		"Skip shutdown detection; use only with synthetic test data",
	)
	if command == "send" || command == "receive" {
		flags.IntVar(&values.port, "port", 7432, "TCP port (1-65535)")
		flags.IntVar(&values.workers, "workers", 0, "Parallel CPU workers (0: runtime CPU budget, 1-256: explicit)")
	}
	if command == "send" {
		flags.StringVar(&since, "since", "", "Select sessions created on or after YYYY-MM-DD (UTC)")
		flags.StringVar(&until, "until", "", "Select sessions created on or before YYYY-MM-DD (UTC)")
		flags.Func("project-prefix", "Select initial project names with this prefix (repeatable, case-sensitive)",
			func(value string) error { prefixes = append(prefixes, value); return nil })
		flags.Func("project-contains", "Select initial project names containing this text (repeatable, case-sensitive)",
			func(value string) error { contains = append(contains, value); return nil })
		flags.StringVar(
			&values.compression,
			"compression",
			"fast",
			"Archive compression: fast, balanced, best, or none",
		)
		flags.DurationVar(&values.wait, "wait", 5*time.Minute, "Maximum wait for receiver startup")
		flags.StringVar(&values.fingerprint, "fingerprint", "", "Optional receiver certificate SHA-256 pin")
	}
	if command == "receive" {
		flags.StringVar(&values.listen, "listen", "0.0.0.0", "Literal local IPv4 or IPv6 bind address")
		flags.Int64Var(&values.maxGiB, "max-gib", 1024, "Maximum received archive size in GiB")
		flags.Var(&values.mappings, "map", "Named destination NAME=/path (repeatable)")
		flags.StringVar(
			&values.importRoot,
			"archive-dir",
			"",
			"Preserve unchanged foreign snapshots (default ~/.codex-transfer/imports)",
		)
		flags.Var(&values.pathMappings, "path-map", "Source path prefix SOURCE=/local/path (repeatable)")
	} else {
		flags.Var(&values.extra, "extra", "Additional source NAME=/path (repeatable)")
	}
	if command == "paths" {
		flags.BoolVar(&values.all, "all", false, "Also show standard locations that do not exist")
	}
	if command == "send" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		return nil, []string{}, err
	}
	positional := flags.Args()
	if command == "send" {
		filter, err := migrate.NewSessionFilter(since, until, prefixes, contains)
		if err != nil {
			return nil, positional, err
		}
		values.filter = filter
		if filter != nil && len(values.extra) > 0 {
			return nil, positional, errors.New("session filtering cannot be combined with extra data roots")
		}
		switch values.compression {
		case "fast", "balanced", "best", "none":
		default:
			return nil, positional, errors.New("compression must be fast, balanced, best, or none")
		}
		if len(positional) != 1 {
			return nil, positional, errors.New("send requires exactly one receiver IP")
		}
		if values.wait <= 0 {
			return nil, positional, errors.New("wait duration must be positive")
		}
	} else if len(positional) != 0 {
		return nil, positional, errors.New("unexpected positional arguments")
	}
	if command == "send" || command == "receive" {
		if values.workers < 0 || values.workers > 256 {
			return nil, positional, errors.New("workers must be between 0 and 256")
		}
		if values.importRoot != "" && !filepath.IsAbs(values.importRoot) {
			return nil, positional, errors.New("archive-dir must be an absolute path")
		}
		if values.port < 1 || values.port > 65535 {
			return nil, positional, errors.New("port must be between 1 and 65535")
		}
	}
	if command == "receive" && (values.maxGiB <= 0 || values.maxGiB > 1<<20) {
		return nil, positional, errors.New("max-gib must be between 1 and 1048576")
	}
	return values, positional, nil
}

// roots resolves standard and explicitly selected locations while protecting the user's home directory.
func (o *options) roots(source bool, receiving bool) ([]migrate.Root, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	roots, err := migrate.Discover(home, o.codexHome, o.sqliteHome, source)
	if err != nil {
		return nil, err
	}
	additions := o.extra
	if receiving {
		additions = o.mappings
	}
	names := map[string]bool{}
	for _, addition := range additions {
		name, path, _ := strings.Cut(addition, "=")
		if names[name] {
			return nil, fmt.Errorf("duplicate path argument for %s", name)
		}
		names[name] = true
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("path for %s must be absolute", name)
		}
		found := false
		for index := range roots {
			if roots[index].Name == name {
				if !receiving {
					return nil, fmt.Errorf("duplicate source root %q", name)
				}
				roots[index].Path = path
				found = true
				break
			}
		}
		if !found {
			roots = append(roots, migrate.Root{Name: name, Path: path})
		}
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root.Path, home)
		if err != nil {
			return nil, err
		}
		if rel == "." || filepath.IsLocal(rel) {
			return nil, fmt.Errorf("refusing home directory or its ancestor as data root: %s", root.Path)
		}
	}
	return roots, migrate.ValidateRoots(roots)
}
