package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// rolloutUUIDLength is the width of the UUID suffix identifying a canonical physical rollout.
const rolloutUUIDLength = 36

// ClearSessions deletes confirmed histories and indexes in private clones before replacing live roots.
// Callers must obtain confirmation and ensure Codex is stopped before invoking this operation.
// Selected records must be unchanged results from ListSessions for the same data roots.
func ClearSessions(ctx context.Context, home, sqliteHome string, selected []SessionUsage) error {
	if len(selected) == 0 {
		return errors.New("no sessions selected for deletion")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	directory, err := sessionSQLiteHome(home, sqliteHome)
	if err != nil {
		return err
	}
	current, err := ListSessions(ctx, home, directory)
	if err != nil {
		return err
	}
	if err := validateSessionDeletion(selected, current); err != nil {
		return err
	}
	if err := validateSessionReferences(selected, current); err != nil {
		return err
	}
	roots := []Root{{Name: "codex", Path: home}}
	if !within(home, directory) {
		if _, err := os.Lstat(directory); err == nil {
			roots = append(roots, Root{Name: "sqlite", Path: directory})
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	staged, err := prepareInstallation(roots)
	if err != nil {
		return err
	}
	defer staged.close()
	staged.sqliteHome = directory
	for _, item := range staged.items {
		_, err := cloneSessionTarget(ctx, item)
		if err != nil {
			return err
		}
	}
	data, err := staged.resolve("codex")
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, session := range selected {
		ids[session.ID] = true
		for _, candidate := range session.files {
			relative, err := filepath.Rel(home, candidate.file)
			if err != nil || !filepath.IsLocal(relative) {
				return errors.New("selected session is outside the Codex root")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(data, relative)); err != nil {
				return err
			}
		}
	}
	databases, err := stagedSessionDatabases(staged)
	if err != nil {
		return err
	}
	for _, file := range databases {
		if err := deleteSessionIndex(ctx, file, ids); err != nil {
			return err
		}
	}
	// Paginated conversation bodies live in a separate database and cannot cascade from state_5.sqlite.
	if err := clearSessionHistoryDatabase(ctx, staged, selectedSessionRolloutIDs(selected)); err != nil {
		return err
	}
	for _, name := range []string{"history.jsonl", "session_index.jsonl"} {
		if err := pruneSessionHistory(ctx, filepath.Join(data, name), ids); err != nil {
			return err
		}
	}
	// Confirmation must still describe the live files after cloning and before any replacement occurs.
	current, err = ListSessions(ctx, home, directory)
	if err != nil {
		return err
	}
	if err := validateSessionDeletion(selected, current); err != nil {
		return err
	}
	if err := validateSessionReferences(selected, current); err != nil {
		return err
	}
	// Replacing a whole root can otherwise overwrite post-clone edits to configuration or unselected histories.
	if err := staged.validateSessionTargets(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := staged.commit(); err != nil {
		return err
	}
	// Successful clearing must reclaim stored histories rather than retain them in migration backups.
	for _, item := range staged.items {
		if item.backup != "" {
			if err := os.RemoveAll(item.backup); err != nil {
				return fmt.Errorf("sessions cleared, but remove retained backup %s: %w", item.backup, err)
			}
		}
	}
	return nil
}

// validateSessionDeletion rejects changed identities, workspaces, file counts, and file snapshots.
func validateSessionDeletion(selected, current []SessionUsage) error {
	groups := map[string]SessionUsage{}
	for _, session := range current {
		groups[session.ID] = session
	}
	for _, before := range selected {
		after, exists := groups[before.ID]
		if !exists || before.Workspace != after.Workspace || before.Files != after.Files ||
			before.Bytes != after.Bytes || len(before.files) != len(after.files) {
			return fmt.Errorf("session %s changed after preview; run the command again", before.ID)
		}
		for index, candidate := range before.files {
			other := after.files[index]
			if candidate.file != other.file || !sameSessionFileState(candidate.info, other.info) {
				return fmt.Errorf("session %s changed after preview; run the command again", before.ID)
			}
		}
	}
	return nil
}

// validateSessionReferences prevents clearing rollout ranges inherited by conversations outside the selection.
func validateSessionReferences(selected, current []SessionUsage) error {
	selectedIDs := map[string]bool{}
	rolloutIDs := selectedSessionRolloutIDs(selected)
	for _, session := range selected {
		selectedIDs[session.ID] = true
	}
	for _, session := range current {
		if selectedIDs[session.ID] {
			continue
		}
		for _, candidate := range session.files {
			base := candidate.meta.HistoryBase
			// A retained archived or compressed fork still needs its inherited source file to resume.
			if base != nil && rolloutIDs[base.ThreadID] {
				return fmt.Errorf(
					"cannot clear selected sessions: forked history in session %s still references rollout %s",
					session.ID,
					base.ThreadID,
				)
			}
		}
	}
	return nil
}

// selectedSessionRolloutIDs includes conversation IDs and the physical histories replaced by reverts.
func selectedSessionRolloutIDs(selected []SessionUsage) map[string]bool {
	ids := map[string]bool{}
	for _, session := range selected {
		ids[session.ID] = true
		for _, candidate := range session.files {
			name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(candidate.file), ".zst"), ".jsonl")
			canonical := strings.HasPrefix(name, "rollout-") && len(name) > rolloutUUIDLength
			// Reverted rollouts keep the conversation ID in metadata but use a different UUID in the filename.
			if canonical && name[len(name)-rolloutUUIDLength-1] == '-' {
				ids[name[len(name)-rolloutUUIDLength:]] = true
			}
		}
	}
	return ids
}
