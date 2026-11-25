package api

import (
	"time"

	"github.com/google/uuid"
)

type InboxPromptOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type InboxItemSummary struct {
	ID              uuid.UUID  `json:"id"`
	Kind            string     `json:"kind" jsonschema:"enum=morning_report,enum=prompt_checkin"`
	LocalDate       string     `json:"local_date"`
	CreatedAt       time.Time  `json:"created_at"`
	Title           string     `json:"title"`
	Preview         string     `json:"preview,omitempty"`
	ReportID        *uuid.UUID `json:"report_id,omitempty"`
	PromptID        *uuid.UUID `json:"prompt_id,omitempty"`
	Status          string     `json:"status,omitempty"`
	Answer          string     `json:"answer,omitempty"`
	AnsweredAt      *time.Time `json:"answered_at,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	Timing          string     `json:"timing,omitempty" jsonschema:"enum=on_time,enum=late"`
	AnsweredChannel string     `json:"answered_channel,omitempty" jsonschema:"enum=ios_app,enum=ios_push,enum=web,enum=telegram"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
}

type InboxFeedResponse struct {
	Items      []InboxItemSummary `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type InboxDetailResponse struct {
	ID              uuid.UUID           `json:"id"`
	Kind            string              `json:"kind" jsonschema:"enum=morning_report,enum=prompt_checkin"`
	LocalDate       string              `json:"local_date"`
	CreatedAt       time.Time           `json:"created_at"`
	Title           string              `json:"title"`
	ReportID        *uuid.UUID          `json:"report_id,omitempty"`
	Body            string              `json:"body,omitempty"`
	Language        string              `json:"language,omitempty"`
	PreparedAt      *time.Time          `json:"prepared_at,omitempty"`
	PromptID        *uuid.UUID          `json:"prompt_id,omitempty"`
	Status          string              `json:"status,omitempty"`
	Answer          string              `json:"answer,omitempty"`
	AnsweredAt      *time.Time          `json:"answered_at,omitempty"`
	ExpiresAt       *time.Time          `json:"expires_at,omitempty"`
	Timing          string              `json:"timing,omitempty" jsonschema:"enum=on_time,enum=late"`
	AnsweredChannel string              `json:"answered_channel,omitempty" jsonschema:"enum=ios_app,enum=ios_push,enum=web,enum=telegram"`
	Options         []InboxPromptOption `json:"options,omitempty"`
	ReadAt          *time.Time          `json:"read_at,omitempty"`
}

type PendingCheckinPrompt struct {
	PromptID  uuid.UUID           `json:"prompt_id"`
	LocalDate string              `json:"local_date"`
	ExpiresAt time.Time           `json:"expires_at"`
	Title     string              `json:"title"`
	Options   []InboxPromptOption `json:"options"`
}

type InboxPendingResponse struct {
	Prompts []PendingCheckinPrompt `json:"prompts"`
}

type PromptAnswerRequest struct {
	Answer  string `json:"answer" jsonschema:"enum=great,enum=ok,enum=meh,enum=sick"`
	Channel string `json:"channel" jsonschema:"enum=ios_app,enum=ios_push,enum=web"`
}

type PromptAnswerResponse struct {
	Status     string    `json:"status" jsonschema:"enum=saved,enum=already_answered"`
	Answer     string    `json:"answer" jsonschema:"enum=great,enum=ok,enum=meh,enum=sick"`
	AnsweredAt time.Time `json:"answered_at"`
	Timing     string    `json:"timing" jsonschema:"enum=on_time,enum=late"`
}

type PromptExpiredResponse struct {
	Status string `json:"status" jsonschema:"enum=expired"`
}
