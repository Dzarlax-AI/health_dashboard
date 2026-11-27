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
	Kind            string     `json:"kind" jsonschema:"enum=morning_report,enum=evening_report,enum=sleep_update,enum=guidance_change,enum=weekly_digest,enum=nudge,enum=prompt_checkin,enum=prompt_wake_feedback,enum=prompt_context"`
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
	ActionRoute     string     `json:"action_route,omitempty"`
}

type InboxFeedResponse struct {
	Items       []InboxItemSummary `json:"items"`
	NextCursor  string             `json:"next_cursor,omitempty"`
	UnreadCount *int               `json:"unread_count,omitempty"`
	SnapshotAt  *time.Time         `json:"snapshot_at,omitempty"`
}

type InboxDetailResponse struct {
	ID              uuid.UUID           `json:"id"`
	Kind            string              `json:"kind" jsonschema:"enum=morning_report,enum=evening_report,enum=sleep_update,enum=guidance_change,enum=weekly_digest,enum=nudge,enum=prompt_checkin,enum=prompt_wake_feedback,enum=prompt_context"`
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
	ActionRoute     string              `json:"action_route,omitempty"`
}

type PendingCheckinPrompt struct {
	PromptID  uuid.UUID           `json:"prompt_id"`
	Kind      string              `json:"kind,omitempty" jsonschema:"enum=prompt_checkin,enum=prompt_wake_feedback,enum=prompt_context"`
	LocalDate string              `json:"local_date"`
	ExpiresAt time.Time           `json:"expires_at"`
	Title     string              `json:"title"`
	Options   []InboxPromptOption `json:"options"`
}

type InboxPendingResponse struct {
	Prompts     []PendingCheckinPrompt `json:"prompts"`
	UnreadCount *int                   `json:"unread_count,omitempty"`
}

type PromptAnswerRequest struct {
	Answer  string `json:"answer" jsonschema:"enum=great,enum=ok,enum=meh,enum=sick,enum=confirmed,enum=earlier,enum=later,enum=returned_to_sleep,enum=poor_sleep_context,enum=stress_context,enum=travel_context,enum=unknown_context,enum=skip_context"`
	Channel string `json:"channel" jsonschema:"enum=ios_app,enum=ios_push,enum=web"`
}

type PromptAnswerResponse struct {
	Status      string    `json:"status" jsonschema:"enum=saved,enum=already_answered"`
	Answer      string    `json:"answer" jsonschema:"enum=great,enum=ok,enum=meh,enum=sick,enum=confirmed,enum=earlier,enum=later,enum=returned_to_sleep,enum=poor_sleep_context,enum=stress_context,enum=travel_context,enum=unknown_context,enum=skip_context"`
	AnsweredAt  time.Time `json:"answered_at"`
	Timing      string    `json:"timing" jsonschema:"enum=on_time,enum=late"`
	UnreadCount *int      `json:"unread_count,omitempty"`
}

type PromptExpiredResponse struct {
	Status string `json:"status" jsonschema:"enum=expired"`
}

type InboxReadResponse struct {
	ID          uuid.UUID `json:"id"`
	ReadAt      time.Time `json:"read_at"`
	UnreadCount int       `json:"unread_count"`
}

type InboxReadAllRequest struct {
	Before *time.Time `json:"before,omitempty"`
}

type InboxReadAllResponse struct {
	Updated     int       `json:"updated"`
	UnreadCount int       `json:"unread_count"`
	Before      time.Time `json:"before"`
}

type NotificationPreferencesResponse struct {
	InboxEnabled                 bool   `json:"inbox_enabled"`
	APNSPushEnabled              bool   `json:"apns_push_enabled"`
	APNsPushConfigured           bool   `json:"apns_push_configured"`
	APNsTransportMode            string `json:"apns_transport_mode" jsonschema:"enum=direct,enum=relay,enum=none"`
	APNsRelayURL                 string `json:"apns_relay_url,omitempty"`
	TelegramConfigured           bool   `json:"telegram_configured"`
	TelegramNotificationsEnabled bool `json:"telegram_notifications_enabled"`
	ActivePushDevicesCount       int  `json:"active_push_devices_count"`
	MorningWeekdayHour           int  `json:"morning_weekday_hour"`
	MorningWeekendHour           int  `json:"morning_weekend_hour"`
	EveningWeekdayHour           int  `json:"evening_weekday_hour"`
	EveningWeekendHour           int  `json:"evening_weekend_hour"`
	DaytimeEnabled               bool `json:"daytime_enabled"`
	DaytimeStartHour             int  `json:"daytime_start_hour"`
	DaytimeEndHour               int  `json:"daytime_end_hour"`
	PushMorningReport            bool `json:"push_morning_report"`
	PushEveningReport            bool `json:"push_evening_report"`
	PushCheckins                 bool `json:"push_checkins"`
	PushDaytimeUpdates           bool `json:"push_daytime_updates"`
	PushDigestsNudges            bool `json:"push_digests_nudges"`
}

type NotificationPreferencesRequest struct {
	TelegramNotificationsEnabled *bool `json:"telegram_notifications_enabled,omitempty"`
	MorningWeekdayHour           *int  `json:"morning_weekday_hour,omitempty"`
	MorningWeekendHour           *int  `json:"morning_weekend_hour,omitempty"`
	EveningWeekdayHour           *int  `json:"evening_weekday_hour,omitempty"`
	EveningWeekendHour           *int  `json:"evening_weekend_hour,omitempty"`
	DaytimeEnabled               *bool `json:"daytime_enabled,omitempty"`
	DaytimeStartHour             *int  `json:"daytime_start_hour,omitempty"`
	DaytimeEndHour               *int  `json:"daytime_end_hour,omitempty"`
	PushMorningReport            *bool `json:"push_morning_report,omitempty"`
	PushEveningReport            *bool `json:"push_evening_report,omitempty"`
	PushCheckins                 *bool `json:"push_checkins,omitempty"`
	PushDaytimeUpdates           *bool `json:"push_daytime_updates,omitempty"`
	PushDigestsNudges            *bool `json:"push_digests_nudges,omitempty"`
}
