package migrate

import "time"

// sessionIndexRecord owns the fixed metadata used only when registering a new receiving thread.
type sessionIndexRecord struct {
	// id is the session identity validated against the selected incoming rollout.
	id string
	// rolloutPath is the final receiving path, not the private staging path.
	rolloutPath string
	// cwd is the latest nonempty working directory in the rollout.
	cwd string
	// cliVersion identifies the client that created the session.
	cliVersion string
	// createdAt is the session header timestamp; SQL projection supplies seconds and milliseconds.
	createdAt time.Time
	// updatedAt is the rollout modification time used for update, recency, and archive timestamps.
	updatedAt time.Time
	// source retains the origin enum as a string or its structured JSON representation.
	source string
	// modelProvider defaults to openai when absent from the header.
	modelProvider string
	// title is initially the ID and becomes the trimmed first user message.
	title string
	// preview is the trimmed first user message, bounded to the title's rune limit.
	preview string
	// firstUserMessage retains the first user event text without trimming or truncation.
	firstUserMessage string
	// hasUserEvent distinguishes an empty first message from a missing user event.
	hasUserEvent bool
	// sandboxPolicy retains the latest sandbox policy as JSON text.
	sandboxPolicy string
	// approvalMode retains the latest approval enum or structured JSON representation.
	approvalMode string
	// archived selects archived state and a nullable archive timestamp at projection.
	archived bool
	// model is nil until a turn supplies a nonempty model name.
	model *string
	// reasoningEffort distinguishes a missing field from an explicitly empty effort.
	reasoningEffort *string
}

// columns is the sole mapping from typed session metadata to the supported receiving SQLite columns.
func (r *sessionIndexRecord) columns() map[string]any {
	var archivedAt any
	if r.archived {
		archivedAt = r.updatedAt.Unix()
	}
	return map[string]any{
		"id":                 r.id,
		"rollout_path":       r.rolloutPath,
		"cwd":                r.cwd,
		"cli_version":        r.cliVersion,
		"created_at":         r.createdAt.Unix(),
		"created_at_ms":      r.createdAt.UnixMilli(),
		"updated_at":         r.updatedAt.Unix(),
		"updated_at_ms":      r.updatedAt.UnixMilli(),
		"recency_at":         r.updatedAt.Unix(),
		"recency_at_ms":      r.updatedAt.UnixMilli(),
		"source":             r.source,
		"model_provider":     r.modelProvider,
		"title":              r.title,
		"preview":            r.preview,
		"first_user_message": r.firstUserMessage,
		"has_user_event":     r.hasUserEvent,
		"sandbox_policy":     r.sandboxPolicy,
		"approval_mode":      r.approvalMode,
		"tokens_used":        0,
		"archived":           r.archived,
		"archived_at":        archivedAt,
		"history_mode":       "legacy",
		"model":              r.model,
		"reasoning_effort":   r.reasoningEffort,
	}
}
