package ui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/notify"
	"health-receiver/internal/storage"
)

type inboxCursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

func (h *Handler) inboxToday(db *storage.DB, schema string, now time.Time) string {
	defaults := storage.NotifyConfig{}
	if h.mgr != nil {
		defaults = h.mgr.NotifyDefaultsFor(schema)
	}
	return db.TenantLocalDate(defaults, now)
}

func (h *Handler) inboxLang(r *http.Request, db *storage.DB) string {
	if q := strings.TrimSpace(r.URL.Query().Get("lang")); q == "en" || q == "ru" || q == "sr" {
		return q
	}
	defaults := storage.NotifyConfig{Lang: "en"}
	if h.mgr != nil {
		defaults = h.mgr.NotifyDefaultsFor(h.tenantSchema(r))
	}
	lang := db.GetNotifyConfig(defaults).Lang
	if lang != "en" && lang != "ru" && lang != "sr" {
		return "en"
	}
	return lang
}

func morningReportInboxTitle(lang string) string {
	switch lang {
	case "ru":
		return "Утренний отчёт"
	case "sr":
		return "Jutarnji izveštaj"
	default:
		return "Morning Report"
	}
}

func inboxPreviewText(plain string) string {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return ""
	}
	lines := strings.Split(plain, "\n")
	parts := make([]string, 0, 2)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
		if len(parts) == 2 {
			break
		}
	}
	joined := strings.Join(parts, " ")
	const maxRunes = 160
	if utf8.RuneCountInString(joined) <= maxRunes {
		return joined
	}
	runes := []rune(joined)
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func promptOptionsForAPI(lang string) (string, []clientapi.InboxPromptOption) {
	title, opts := notify.CheckinPromptOptions(lang)
	out := make([]clientapi.InboxPromptOption, len(opts))
	for i, o := range opts {
		out[i] = clientapi.InboxPromptOption{ID: o.ID, Label: o.Label}
	}
	return title, out
}

func checkinTimingForStatus(status string) string {
	switch status {
	case storage.CheckinStatusAnswered:
		return storage.CheckinTimingOnTime
	case storage.CheckinStatusLateAnswered:
		return storage.CheckinTimingLate
	default:
		return ""
	}
}

func effectiveCheckinStatus(status, localDate, today string) string {
	if status == storage.CheckinStatusPrompted && localDate != today {
		return storage.CheckinStatusExpired
	}
	return status
}

func (h *Handler) inboxList(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	qv := r.URL.Query()
	limit := 20
	if rawLimit := strings.TrimSpace(qv.Get("limit")); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 || n > 100 {
			jsonError(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	q := storage.InboxQuery{Limit: limit + 1}
	if raw := strings.TrimSpace(qv.Get("cursor")); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var c inboxCursor
		if err != nil || json.Unmarshal(data, &c) != nil || c.At.IsZero() || c.ID == uuid.Nil {
			jsonError(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		q.BeforeAt = &c.At
		q.BeforeID = c.ID
	}
	rows, err := db.ListInboxItems(r.Context(), q)
	if err != nil {
		jsonError(w, "failed to load inbox", http.StatusInternalServerError)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		data, _ := json.Marshal(inboxCursor{At: last.CreatedAt, ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	lang := h.inboxLang(r, db)
	today := h.inboxToday(db, h.tenantSchema(r), time.Now())
	promptTitle, _ := promptOptionsForAPI(lang)
	items := make([]clientapi.InboxItemSummary, 0, len(rows))
	for _, row := range rows {
		item := clientapi.InboxItemSummary{
			ID:        row.ID,
			Kind:      row.Kind,
			LocalDate: row.LocalDate,
			CreatedAt: row.CreatedAt,
			ReadAt:    row.ReadAt,
		}
		switch row.Kind {
		case storage.InboxKindMorningReport:
			reportLang := row.ReportLanguage
			if reportLang == "" {
				reportLang = lang
			}
			item.Title = morningReportInboxTitle(reportLang)
			item.Preview = inboxPreviewText(storage.NormalizeReportPlainText(row.ReportPlainBody))
			item.ReportID = row.ReportID
		case storage.InboxKindPromptCheckin:
			id := row.ID
			status := effectiveCheckinStatus(row.CheckinStatus, row.LocalDate, today)
			item.Title = promptTitle
			item.PromptID = &id
			item.Status = status
			item.Answer = row.CheckinAnswer
			item.AnsweredAt = row.CheckinAnswered
			item.ExpiresAt = row.CheckinExpires
			item.Timing = checkinTimingForStatus(status)
			item.AnsweredChannel = row.CheckinChannel
		}
		items = append(items, item)
	}
	jsonResponse(w, clientapi.InboxFeedResponse{Items: items, NextCursor: next})
}

func (h *Handler) inboxPending(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	today := h.inboxToday(db, h.tenantSchema(r), now)
	rows, err := db.ListPendingInboxCheckins(r.Context(), today, now)
	if err != nil {
		jsonError(w, "failed to load pending prompts", http.StatusInternalServerError)
		return
	}
	lang := h.inboxLang(r, db)
	title, options := promptOptionsForAPI(lang)
	prompts := make([]clientapi.PendingCheckinPrompt, 0, len(rows))
	for _, row := range rows {
		if row.CheckinExpires == nil {
			continue
		}
		prompts = append(prompts, clientapi.PendingCheckinPrompt{
			PromptID:  row.ID,
			LocalDate: row.LocalDate,
			ExpiresAt: *row.CheckinExpires,
			Title:     title,
			Options:   options,
		})
	}
	jsonResponse(w, clientapi.InboxPendingResponse{Prompts: prompts})
}

func (h *Handler) inboxDetail(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	row, err := db.GetInboxItem(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonError(w, "failed to load inbox item", http.StatusInternalServerError)
		return
	}
	lang := h.inboxLang(r, db)
	resp := clientapi.InboxDetailResponse{
		ID:        row.ID,
		Kind:      row.Kind,
		LocalDate: row.LocalDate,
		CreatedAt: row.CreatedAt,
		ReadAt:    row.ReadAt,
	}
	switch row.Kind {
	case storage.InboxKindMorningReport:
		reportLang := row.ReportLanguage
		if reportLang == "" {
			reportLang = lang
		}
		resp.Title = morningReportInboxTitle(reportLang)
		resp.ReportID = row.ReportID
		resp.Body = storage.NormalizeReportPlainText(row.ReportPlainBody)
		resp.Language = reportLang
		resp.PreparedAt = row.ReportPrepared
	case storage.InboxKindPromptCheckin:
		title, options := promptOptionsForAPI(lang)
		pid := row.ID
		today := h.inboxToday(db, h.tenantSchema(r), time.Now())
		status := effectiveCheckinStatus(row.CheckinStatus, row.LocalDate, today)
		resp.Title = title
		resp.PromptID = &pid
		resp.Status = status
		resp.Answer = row.CheckinAnswer
		resp.AnsweredAt = row.CheckinAnswered
		resp.ExpiresAt = row.CheckinExpires
		resp.Timing = checkinTimingForStatus(status)
		resp.AnsweredChannel = row.CheckinChannel
		if row.LocalDate == today && (status == storage.CheckinStatusPrompted || status == storage.CheckinStatusExpired) {
			resp.Options = options
		}
	}
	jsonResponse(w, resp)
}

func (h *Handler) promptAnswer(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	promptID, err := uuid.Parse(r.PathValue("prompt_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req clientapi.PromptAnswerRequest
	const maxBodyBytes = 1024
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	req.Answer = strings.TrimSpace(req.Answer)
	req.Channel = strings.TrimSpace(req.Channel)
	if err := storage.ValidateCheckinAnswer(req.Answer); err != nil {
		jsonError(w, "invalid answer", http.StatusBadRequest)
		return
	}
	if err := storage.ValidateClientCheckinChannel(req.Channel); err != nil {
		jsonError(w, "invalid channel", http.StatusBadRequest)
		return
	}

	schema := h.tenantSchema(r)
	if h.mgr != nil {
		if mu := h.mgr.CheckinSendMuFor(schema); mu != nil {
			mu.Lock()
			defer mu.Unlock()
		}
	}
	now := time.Now()
	today := h.inboxToday(db, schema, now)
	res, err := db.AnswerCheckinByPromptID(r.Context(), promptID, today, req.Channel, req.Answer, now)
	if errors.Is(err, storage.ErrCheckinPromptNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, storage.ErrCheckinPromptExpired) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(clientapi.PromptExpiredResponse{Status: storage.CheckinAnswerStatusExpired})
		return
	}
	if err != nil {
		jsonError(w, "failed to save prompt answer", http.StatusInternalServerError)
		return
	}

	if res.Status == storage.CheckinAnswerStatusSaved && res.Timing == storage.CheckinTimingOnTime {
		if evidenceErr := db.RecordWakeCheckinEvidence(res.Date, res.AnsweredAt); evidenceErr != nil {
			log.Printf("checkin: record wake evidence for %s: %v", res.Date, evidenceErr)
		}
		h.triggerCheckinRefresh(db, schema)
	}

	jsonResponse(w, clientapi.PromptAnswerResponse{
		Status:     res.Status,
		Answer:     res.Answer,
		AnsweredAt: res.AnsweredAt,
		Timing:     res.Timing,
	})
}

func (h *Handler) triggerCheckinRefresh(db *storage.DB, schema string) {
	if h.onCheckinAnswered != nil {
		h.onCheckinAnswered(db, schema)
		return
	}
	if h.mgr != nil {
		if refresh := h.mgr.BackfillDatesFor(schema); refresh != nil {
			refresh(nil)
		}
	}
}
