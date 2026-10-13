package migrate

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateSessionReferences prevents clearing rollout ranges inherited by conversations outside the selection.
func validateSessionReferences(selected, current []SessionUsage) error {
	selectedIDs := map[string]bool{}
	rolloutIDs := map[string]bool{}
	for _, session := range selected {
		selectedIDs[session.ID] = true
		rolloutIDs[session.ID] = true
		for _, candidate := range session.files {
			name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(candidate.file), ".zst"), ".jsonl")
			// Reverted rollouts keep the conversation ID in metadata but use a different UUID in the filename.
			if strings.HasPrefix(name, "rollout-") && len(name) > 36 && name[len(name)-37] == '-' {
				rolloutIDs[name[len(name)-36:]] = true
			}
		}
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
