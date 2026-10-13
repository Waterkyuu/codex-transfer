package migrate

import (
	"fmt"
	"path/filepath"
	"strings"
)

// rolloutUUIDLength is the width of the UUID suffix identifying a canonical physical rollout.
const rolloutUUIDLength = 36

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
