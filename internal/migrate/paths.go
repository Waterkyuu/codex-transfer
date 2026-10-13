package migrate

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// sourceArchiveRoot is reserved for a receiver-owned copy of the unchanged source snapshot.
	sourceArchiveRoot = "transfer-source-archive"
)

// pathMapping translates a canonical source prefix to a destination chosen on this computer.
type pathMapping struct {
	// source is normalized according to the source OS, independently of the host runtime.
	source string
	// target is a local absolute path, never a sender-controlled destination.
	target string
}

// pathMapper translates declared path values without replacing substrings in conversation content.
type pathMapper struct {
	// sourceOS determines drive, separator, and case handling.
	sourceOS string
	// mappings are ordered longest-first so explicit roots override the home mapping.
	mappings []pathMapping
	// rollouts maps compressed destination files to their adapted plain siblings; populated before SQLite updates.
	rollouts map[string]string
	// statePaths supplies receiver-selected SQLite/log roots, including relative config and environment overrides.
	statePaths map[string]string
}

// supportedOS limits platform negotiation to the operating systems supported by released binaries.
func supportedOS(value string) bool {
	return value == "darwin" || value == "linux" || value == "windows"
}

// sourcePath canonicalizes path syntax without consulting or following the receiver's filesystem.
func sourcePath(value, sourceOS string) string {
	if sourceOS == "windows" {
		value = strings.ReplaceAll(value, `\`, "/")
		if strings.HasPrefix(value, "//?/UNC/") {
			value = "//" + strings.TrimPrefix(value, "//?/UNC/")
		} else {
			value = strings.TrimPrefix(value, "//?/")
		}
		absolute := len(value) >= 3 && value[1] == ':' && value[2] == '/' || strings.HasPrefix(value, "//")
		if !absolute {
			return ""
		}
		return path.Clean(value)
	}
	if !path.IsAbs(value) {
		return ""
	}
	return path.Clean(value)
}

// newPathMapper validates destination paths and establishes deterministic longest-prefix matching.
func newPathMapper(sourceOS string, mappings map[string]string) (*pathMapper, error) {
	if !supportedOS(sourceOS) {
		return nil, errors.New("unsupported source operating system")
	}
	mapper := &pathMapper{sourceOS: sourceOS, rollouts: map[string]string{}, statePaths: map[string]string{}}
	seen := map[string]bool{}
	for source, target := range mappings {
		canonical := sourcePath(source, sourceOS)
		if canonical == "" || !filepath.IsAbs(target) {
			return nil, fmt.Errorf("path mapping requires absolute source and destination: %q", source)
		}
		key := canonical
		if sourceOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate source path mapping %q", source)
		}
		seen[key] = true
		mapper.mappings = append(mapper.mappings, pathMapping{source: canonical, target: filepath.Clean(target)})
	}
	sort.Slice(
		mapper.mappings,
		func(i, j int) bool { return len(mapper.mappings[i].source) > len(mapper.mappings[j].source) },
	)
	return mapper, nil
}

// remap rewrites complete path prefixes while preserving unrelated strings byte-for-byte.
func (m *pathMapper) remap(value string) string {
	canonical := sourcePath(value, m.sourceOS)
	if canonical == "" {
		return value
	}
	for _, mapping := range m.mappings {
		candidate, prefix := canonical, mapping.source
		if m.sourceOS == "windows" {
			candidate, prefix = strings.ToLower(candidate), strings.ToLower(prefix)
		}
		if candidate == prefix {
			return mapping.target
		}
		if strings.HasPrefix(candidate, strings.TrimSuffix(prefix, "/")+"/") {
			suffix := strings.TrimPrefix(canonical[len(mapping.source):], "/")
			return filepath.Join(mapping.target, filepath.FromSlash(suffix))
		}
	}
	return value
}

// receivePlan separates active destinations from roots retained only in the original source archive.
type receivePlan struct {
	// roots contain only receiver-selected destinations and the optional preservation directory.
	roots []Root
	// archived identifies source roots whose platform-specific contents must not replace active state.
	archived map[string]bool
	// cross records whether platform adaptation is required.
	cross bool
}

// portableRoot identifies state that has a shared meaning across the supported operating systems.
func portableRoot(name string) bool {
	switch name {
	case "codex", "agents", "workspace", "sqlite", "logs":
		return true
	default:
		return false
	}
}

// planDestinations preserves foreign platform roots unless the receiver explicitly maps them.
func planDestinations(
	sourceOS, targetOS string,
	names []string,
	targets, overrides map[string]string,
	imports string,
) (*receivePlan, error) {
	if !supportedOS(sourceOS) || !supportedOS(targetOS) {
		return nil, errors.New("unsupported source or destination operating system")
	}
	plan := &receivePlan{archived: map[string]bool{}, cross: sourceOS != targetOS}
	seen := map[string]bool{}
	for _, name := range names {
		if !validName(name) || name == sourceArchiveRoot || seen[name] {
			return nil, fmt.Errorf("invalid or duplicate source root %q", name)
		}
		seen[name] = true
		target, explicit := overrides[name]
		if !explicit && plan.cross && !portableRoot(name) {
			plan.archived[name] = true
			continue
		}
		if !explicit {
			target = targets[name]
		}
		if target == "" && plan.cross && (name == "sqlite" || name == "logs") && targets["codex"] != "" {
			target = targets["codex"] + "." + name
		}
		if target == "" {
			return nil, fmt.Errorf("root %q has no destination; restart receiver with --map %s=/path", name, name)
		}
		plan.roots = append(plan.roots, Root{Name: name, Path: target})
	}
	if plan.cross {
		if !filepath.IsAbs(imports) {
			return nil, errors.New("cross-system migration requires an absolute source archive directory")
		}
		plan.roots = append(plan.roots, Root{Name: sourceArchiveRoot, Path: filepath.Join(imports, sourceOS)})
	}
	if err := ValidateRoots(plan.roots); err != nil {
		return nil, err
	}
	return plan, nil
}
