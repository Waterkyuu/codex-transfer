package migrate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractPolicy confines portability operations to the authenticated roots in receiver-owned staging.
type extractPolicy struct {
	// archived identifies roots retained unchanged in the complete source archive.
	archived map[string]bool
	// mapper translates source path syntax to selected local destinations.
	mapper *pathMapper
	// installation resolves selected destinations back into private staging.
	installation *installation
	// cross enables only the adaptations required by a different operating system.
	cross bool
	// sourcePaths locates relative source links independently of the host's path syntax.
	sourcePaths map[string]string
}

// windowsArchiveName rejects unrepresentable names before destination replacement instead of losing data.
func windowsArchiveName(name string) error {
	for _, part := range strings.Split(name, "/") {
		if strings.ContainsAny(part, `<>:"|?*`) || strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return fmt.Errorf("source filename is not representable on Windows: %q", name)
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		reserved := base == "CON" || base == "PRN" || base == "AUX" || base == "NUL"
		if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
			reserved = reserved || base[3] >= '1' && base[3] <= '9'
		}
		if reserved {
			return fmt.Errorf("source filename is reserved on Windows: %q", name)
		}
	}
	return nil
}

// linkSource resolves a link only into a selected staging tree and never into external live content.
func (p *extractPolicy) linkSource(link pendingLink) (string, error) {
	original := link.target
	if sourcePath(original, p.mapper.sourceOS) == "" {
		root := sourcePath(p.sourcePaths[link.root], p.mapper.sourceOS)
		if root == "" {
			return "", errors.New("relative link has no source root metadata")
		}
		original = path.Join(root, path.Dir(link.relative), strings.ReplaceAll(link.target, `\`, "/"))
	}
	mapped := p.mapper.remap(original)
	for _, item := range p.installation.items {
		if item.name == sourceArchiveRoot || !within(item.target, mapped) {
			continue
		}
		relative, err := filepath.Rel(item.target, mapped)
		if err != nil {
			return "", err
		}
		return filepath.Join(item.slot, "data", relative), nil
	}
	return "", fmt.Errorf(
		"link %q points outside transferred data; include its target with --extra and --map",
		link.relative,
	)
}

// materializeLink replaces selected internal links with files or directories on Windows without link privileges.
func (p *extractPolicy) materializeLink(link pendingLink, links []pendingLink, visiting map[string]bool) error {
	if _, err := os.Lstat(link.path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if visiting[link.path] {
		return fmt.Errorf("cyclic symbolic link %q", link.relative)
	}
	visiting[link.path] = true
	defer delete(visiting, link.path)
	source, err := p.linkSource(link)
	if err != nil {
		return err
	}
	if within(source, link.path) {
		return fmt.Errorf("symbolic link would recursively copy itself: %q", link.relative)
	}
	for _, pending := range links {
		if pending.path == source || within(source, pending.path) {
			if err := p.materializeLink(pending, links, visiting); err != nil {
				return err
			}
		}
	}
	return copyStagedTree(source, link.path)
}

// copyStagedTree copies only regular files and validated directories, without following symbolic links.
func copyStagedTree(source, target string) error {
	return filepath.WalkDir(source, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, file)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.Mkdir(destination, 0700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("link target contains unsupported non-regular data")
		}
		// #nosec G304 G122 -- Both paths are validated entries inside receiver-owned private staging.
		input, err := os.Open(file)
		if err != nil {
			return err
		}
		// #nosec G304 -- The destination is a new sibling inside private staging, never a live path.
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, input.Close(), output.Close())
	})
}
