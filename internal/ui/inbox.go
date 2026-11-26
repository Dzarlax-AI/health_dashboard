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

const inboxContractHeader = "X-Health-Inbox-Contract"

type inboxCursor struct {
	At time.Time `json:"at"`
	ID uuid.UUID `json:"id"`
}

func inboxContractVersion(r *http.Request) int {
	if strings.TrimSpace(r.Header.Get(inboxContractHeader)) == "2" {
		return storage.InboxContractV2
	}
	return storage.InboxContractV1
}

func (h *Handler) inboxLocation(db *storage.DB, schema string) *time.Location {
	defaults := storage.NotifyConfig{}
	if h.mgr != nil {
		defaults = h.mgr.NotifyDefaultsFor(schema)
	}
	tz := defaults.Timezone
	if db != nil {
		tz = db.GetNotifyConfig(defaults).Timezone
	}
	return storage.ResolveReportLocation(tz)
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

func reportInboxTitle(kind, fallbackReason, lang string) string {
	switch kind {
	case storage.InboxKindMorningReport:
		switch lang {
		case "ru":
			return "Утренний отчёт"
		case "sr":
			return "Jutarnji izveštaj"
		default:
			return "Morning Report"
		}
	case storage.InboxKindEveningReport:
		switch lang {
		case "ru":
			return "Вечерний отчёт"
		case "sr":
			return "Večernji izveštaj"
		default:
			return "Evening Report"
		}
	case storage.InboxKindSleepUpdate:
		switch lang {
		case "ru":
			return "Обновлённый анализ сна"
		case "sr":
			return "Ažurirana analiza sna"
		default:
			return "Updated Sleep Analysis"
		}
	case storage.InboxKindGuidanceChange:
		switch lang {
		case "ru":
			return "Рекомендация изменилась"
		case "sr":
			return "Preporuka je promenjena"
		default:
			return "Guidance Updated"
		}
	case storage.InboxKindWeeklyDigest:
		switch lang {
		case "ru":
			return "Еженедельный дайджест"
		case "sr":
			return "Nedeljni pregled"
		default:
			return "Weekly Digest"
		}
	case storage.InboxKindNudge:
		switch fallbackReason {
		case "energy_backfill":
			switch lang {
			case "ru":
				return "Калибровка Energy Bank"
			case "sr":
				return "Kalibracija Energy Bank-a"
			default:
				return "Energy Bank Calibration"
			}
		case "stress_validation_validated":
			switch lang {
			case "ru":
				return "Валидация стресса завершена"
			case "sr":
				return "Validacija stresa završena"
			default:
				return "Stress Validation Ready"
			}
		default:
			switch lang {
			case "ru":
				return "Рекомендация"
			case "sr":
				return "Preporuka"
			default:
				return "Health Note"
			}
		}
	default:
		return kind
	}
}

func actionRouteForInboxRow(row storage.InboxRow) string {
	switch row.Kind {
	case storage.InboxKindGuidanceChange:
		return "/"
	case storage.InboxKindNudge:
		switch row.ReportFallbackReason {
		case "energy_backfill":
			return "/settings"
		case "stress_validation_validated":
			return "/admin"
		}
	}
	return ""
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

func toAPIPromptOptions(opts []notify.CheckinOption) []clientapi.InboxPromptOption {
	out := make([]clientapi.InboxPromptOption, len(opts))
	for i, o := range opts {
		out[i] = clientapi.InboxPromptOption{ID: o.ID, Label: o.Label}
	}
	return out
}

func promptOptionsForAPI(lang string) (string, []clientapi.InboxPromptOption) {
	title, opts := notify.CheckinPromptOptions(lang)
	return title, toAPIPromptOptions(opts)
}

func wakeFeedbackProposedTime(raw json.RawMessage, loc *time.Location) time.Time {
	var rawTS string
	if json.Unmarshal(raw, &rawTS) == nil && rawTS != "" {
		if t, err := time.Parse(time.RFC3339, rawTS); err == nil {
			return t.In(loc)
		}
	}
	return time.Time{}
}

func wakeFeedbackOptionsForAPI(lang string, rawProposed json.RawMessage, loc *time.Location) (string, []clientapi.InboxPromptOption) {
	title, opts := notify.WakeFeedbackPromptOptions(lang, wakeFeedbackProposedTime(rawProposed, loc))
	return title, toAPIPromptOptions(opts)
}

func contextPromptOptionsForAPI(lang string, allowed []string) (string, []clientapi.InboxPromptOption) {
	title, opts := notify.ContextPromptOptions(lang, allowed)
	return title, toAPIPromptOptions(opts)
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

func effectiveWakeFeedbackStatus(response, localDate, today string) string {
	if response != "" {
		return storage.CheckinStatusAnswered
	}
	if localDate != today {
		return storage.CheckinStatusExpired
	}
	return storage.CheckinStatusPrompted
}

func effectiveContextPromptStatus(status string, expiresAt *time.Time, now time.Time) string {
	if status == storage.ContextPromptStatusPrompted && expiresAt != nil && !now.Before(*expiresAt) {
		return storage.ContextPromptStatusExpired
	}
	return status
}

func endOfLocalDay(localDate string, loc *time.Location) time.Time {
	if t, err := time.ParseInLocation("2006-01-02", localDate, loc); err == nil {
		return t.AddDate(0, 0, 1).UTC()
	}
	return time.Now().UTC().Add(12 * time.Hour)
}

func (h *Handler) inboxList(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	ver := inboxContractVersion(r)
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
	q := storage.InboxQuery{
		Limit:           limit + 1,
		ContractVersion: ver,
	}
	if ver >= storage.InboxContractV2 {
		if filter := strings.TrimSpace(qv.Get("filter")); filter != "" {
			switch filter {
			case "all":
			case "unread":
				q.UnreadOnly = true
			default:
				jsonError(w, "invalid filter", http.StatusBadRequest)
				return
			}
		}
		if kind := strings.TrimSpace(qv.Get("kind")); kind != "" {
			if !storage.IsValidInboxKind(kind) {
				jsonError(w, "invalid kind", http.StatusBadRequest)
				return
			}
			q.Kind = kind
		}
	}
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
	rows, snapshotAt, err := db.ListInboxItemsWithSnapshot(r.Context(), q)
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
	schema := h.tenantSchema(r)
	lang := h.inboxLang(r, db)
	now := time.Now()
	loc := h.inboxLocation(db, schema)
	today := h.inboxToday(db, schema, now)
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
		if storage.IsReportInboxKind(row.Kind) {
			reportLang := row.ReportLanguage
			if reportLang == "" {
				reportLang = lang
			}
			item.Title = reportInboxTitle(row.Kind, row.ReportFallbackReason, reportLang)
			item.Preview = inboxPreviewText(storage.NormalizeReportPlainText(row.ReportPlainBody))
			item.ReportID = row.ReportID
			if ver >= storage.InboxContractV2 {
				item.ActionRoute = actionRouteForInboxRow(row)
			}
		} else {
			id := row.ID
			item.PromptID = &id
			switch row.Kind {
			case storage.InboxKindPromptCheckin:
				status := effectiveCheckinStatus(row.CheckinStatus, row.LocalDate, today)
				item.Title = promptTitle
				item.Status = status
				item.Answer = row.CheckinAnswer
				item.AnsweredAt = row.CheckinAnswered
				item.ExpiresAt = row.CheckinExpires
				item.Timing = checkinTimingForStatus(status)
				item.AnsweredChannel = row.CheckinChannel
			case storage.InboxKindPromptWakeFeedback:
				wfTitle, _ := wakeFeedbackOptionsForAPI(lang, row.FeedbackProposedValue, loc)
				exp := endOfLocalDay(row.LocalDate, loc)
				item.Title = wfTitle
				item.Status = effectiveWakeFeedbackStatus(row.FeedbackResponse, row.LocalDate, today)
				item.Answer = row.FeedbackResponse
				item.AnsweredAt = row.FeedbackAnsweredAt
				item.ExpiresAt = &exp
				item.AnsweredChannel = row.FeedbackChannel
			case storage.InboxKindPromptContext:
				cpTitle, _ := contextPromptOptionsForAPI(lang, row.ContextAllowed)
				item.Title = cpTitle
				item.Status = effectiveContextPromptStatus(row.ContextStatus, row.ContextExpiresAt, now)
				item.Answer = row.ContextCategory
				item.AnsweredAt = row.ContextAnsweredAt
				item.ExpiresAt = row.ContextExpiresAt
				item.AnsweredChannel = row.ContextAnsweredChan
			}
		}
		items = append(items, item)
	}

	resp := clientapi.InboxFeedResponse{Items: items, NextCursor: next}
	if ver >= storage.InboxContractV2 {
		if unread, countErr := db.CountUnreadInboxItems(r.Context(), ver); countErr == nil {
			resp.UnreadCount = &unread
		}
		resp.SnapshotAt = &snapshotAt
	}
	jsonResponse(w, resp)
}

func (h *Handler) inboxPending(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	ver := inboxContractVersion(r)
	schema := h.tenantSchema(r)
	now := time.Now()
	loc := h.inboxLocation(db, schema)
	today := h.inboxToday(db, schema, now)
	rows, err := db.ListPendingInboxPrompts(r.Context(), today, now, ver)
	if err != nil {
		jsonError(w, "failed to load pending prompts", http.StatusInternalServerError)
		return
	}
	lang := h.inboxLang(r, db)
	checkinTitle, checkinOptions := promptOptionsForAPI(lang)
	prompts := make([]clientapi.PendingCheckinPrompt, 0, len(rows))
	for _, row := range rows {
		switch row.Kind {
		case storage.InboxKindPromptCheckin:
			if row.CheckinExpires == nil {
				continue
			}
			p := clientapi.PendingCheckinPrompt{
				PromptID:  row.ID,
				LocalDate: row.LocalDate,
				ExpiresAt: *row.CheckinExpires,
				Title:     checkinTitle,
				Options:   checkinOptions,
			}
			if ver >= storage.InboxContractV2 {
				p.Kind = row.Kind
			}
			prompts = append(prompts, p)
		case storage.InboxKindPromptWakeFeedback:
			title, opts := wakeFeedbackOptionsForAPI(lang, row.FeedbackProposedValue, loc)
			prompts = append(prompts, clientapi.PendingCheckinPrompt{
				PromptID:  row.ID,
				Kind:      row.Kind,
				LocalDate: row.LocalDate,
				ExpiresAt: endOfLocalDay(row.LocalDate, loc),
				Title:     title,
				Options:   opts,
			})
		case storage.InboxKindPromptContext:
			if row.ContextExpiresAt == nil {
				continue
			}
			title, opts := contextPromptOptionsForAPI(lang, row.ContextAllowed)
			prompts = append(prompts, clientapi.PendingCheckinPrompt{
				PromptID:  row.ID,
				Kind:      row.Kind,
				LocalDate: row.LocalDate,
				ExpiresAt: *row.ContextExpiresAt,
				Title:     title,
				Options:   opts,
			})
		}
	}
	resp := clientapi.InboxPendingResponse{Prompts: prompts}
	if ver >= storage.InboxContractV2 {
		if unread, countErr := db.CountUnreadInboxItems(r.Context(), ver); countErr == nil {
			resp.UnreadCount = &unread
		}
	}
	jsonResponse(w, resp)
}

func (h *Handler) inboxDetail(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	ver := inboxContractVersion(r)
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
	if ver < storage.RequiredInboxContractVersion(row.Kind) {
		http.NotFound(w, r)
		return
	}
	schema := h.tenantSchema(r)
	lang := h.inboxLang(r, db)
	now := time.Now()
	loc := h.inboxLocation(db, schema)
	today := h.inboxToday(db, schema, now)
	resp := clientapi.InboxDetailResponse{
		ID:        row.ID,
		Kind:      row.Kind,
		LocalDate: row.LocalDate,
		CreatedAt: row.CreatedAt,
		ReadAt:    row.ReadAt,
	}
	if storage.IsReportInboxKind(row.Kind) {
		reportLang := row.ReportLanguage
		if reportLang == "" {
			reportLang = lang
		}
		resp.Title = reportInboxTitle(row.Kind, row.ReportFallbackReason, reportLang)
		resp.ReportID = row.ReportID
		resp.Body = storage.NormalizeReportPlainText(row.ReportPlainBody)
		resp.Language = reportLang
		resp.PreparedAt = row.ReportPrepared
		if ver >= storage.InboxContractV2 {
			resp.ActionRoute = actionRouteForInboxRow(*row)
		}
	} else {
		pid := row.ID
		resp.PromptID = &pid
		switch row.Kind {
		case storage.InboxKindPromptCheckin:
			title, options := promptOptionsForAPI(lang)
			status := effectiveCheckinStatus(row.CheckinStatus, row.LocalDate, today)
			resp.Title = title
			resp.Status = status
			resp.Answer = row.CheckinAnswer
			resp.AnsweredAt = row.CheckinAnswered
			resp.ExpiresAt = row.CheckinExpires
			resp.Timing = checkinTimingForStatus(status)
			resp.AnsweredChannel = row.CheckinChannel
			if row.LocalDate == today && (status == storage.CheckinStatusPrompted || status == storage.CheckinStatusExpired) {
				resp.Options = options
			}
		case storage.InboxKindPromptWakeFeedback:
			title, options := wakeFeedbackOptionsForAPI(lang, row.FeedbackProposedValue, loc)
			status := effectiveWakeFeedbackStatus(row.FeedbackResponse, row.LocalDate, today)
			exp := endOfLocalDay(row.LocalDate, loc)
			resp.Title = title
			resp.Status = status
			resp.Answer = row.FeedbackResponse
			resp.AnsweredAt = row.FeedbackAnsweredAt
			resp.ExpiresAt = &exp
			resp.AnsweredChannel = row.FeedbackChannel
			if row.LocalDate == today && status == storage.CheckinStatusPrompted {
				resp.Options = options
			}
		case storage.InboxKindPromptContext:
			title, options := contextPromptOptionsForAPI(lang, row.ContextAllowed)
			status := effectiveContextPromptStatus(row.ContextStatus, row.ContextExpiresAt, now)
			resp.Title = title
			resp.Status = status
			resp.Answer = row.ContextCategory
			resp.AnsweredAt = row.ContextAnsweredAt
			resp.ExpiresAt = row.ContextExpiresAt
			resp.AnsweredChannel = row.ContextAnsweredChan
			if status == storage.ContextPromptStatusPrompted {
				resp.Options = options
			}
		}
	}
	jsonResponse(w, resp)
}

func (h *Handler) inboxMarkRead(w http.ResponseWriter, r *http.Request) {
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
	ver := inboxContractVersion(r)
	res, err := db.MarkInboxItemRead(r.Context(), id, ver)
	if errors.Is(err, storage.ErrCheckinPromptNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonError(w, "failed to mark inbox item read", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, clientapi.InboxReadResponse{
		ID:          res.ID,
		ReadAt:      res.ReadAt,
		UnreadCount: res.UnreadCount,
	})
}

func (h *Handler) inboxReadAll(w http.ResponseWriter, r *http.Request) {
	db := h.tenantDB(r)
	if !storage.IsInAppInboxEnabled(db) {
		http.NotFound(w, r)
		return
	}
	var req clientapi.InboxReadAllRequest
	if r.Body != nil {
		const maxBodyBytes = 1024
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); err != nil && !errors.Is(err, io.EOF) {
			jsonError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
	}
	ver := inboxContractVersion(r)
	res, err := db.MarkAllInboxRead(r.Context(), req.Before, ver)
	if err != nil {
		jsonError(w, "failed to mark inbox read", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, clientapi.InboxReadAllResponse{
		Updated:     res.Updated,
		UnreadCount: res.UnreadCount,
		Before:      res.Before,
	})
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
	if err := storage.ValidateClientCheckinChannel(req.Channel); err != nil {
		jsonError(w, "invalid channel", http.StatusBadRequest)
		return
	}

	item, err := db.GetInboxItem(r.Context(), promptID)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonError(w, "failed to load prompt", http.StatusInternalServerError)
		return
	}
	if !storage.IsPromptInboxKind(item.Kind) {
		http.NotFound(w, r)
		return
	}

	ver := inboxContractVersion(r)
	schema := h.tenantSchema(r)
	now := time.Now()
	today := h.inboxToday(db, schema, now)

	writeExpired := func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(clientapi.PromptExpiredResponse{Status: storage.CheckinAnswerStatusExpired})
	}
	unreadPtr := func() *int {
		if ver < storage.InboxContractV2 {
			return nil
		}
		if n, countErr := db.CountUnreadInboxItems(r.Context(), ver); countErr == nil {
			return &n
		}
		return nil
	}

	switch item.Kind {
	case storage.InboxKindPromptCheckin:
		if err := storage.ValidateCheckinAnswer(req.Answer); err != nil {
			jsonError(w, "invalid answer", http.StatusBadRequest)
			return
		}
		if h.mgr != nil {
			if mu := h.mgr.CheckinSendMuFor(schema); mu != nil {
				mu.Lock()
				defer mu.Unlock()
			}
		}
		res, err := db.AnswerCheckinByPromptID(r.Context(), promptID, today, req.Channel, req.Answer, now)
		if errors.Is(err, storage.ErrCheckinPromptNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, storage.ErrCheckinPromptExpired) {
			writeExpired()
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
			Status:      res.Status,
			Answer:      res.Answer,
			AnsweredAt:  res.AnsweredAt,
			Timing:      res.Timing,
			UnreadCount: unreadPtr(),
		})

	case storage.InboxKindPromptWakeFeedback:
		if err := storage.ValidateWakeFeedbackResponse(req.Answer); err != nil {
			jsonError(w, "invalid answer", http.StatusBadRequest)
			return
		}
		res, err := db.AnswerWakeFeedbackByPromptID(r.Context(), promptID, today, req.Channel, req.Answer, now)
		if errors.Is(err, storage.ErrCheckinPromptNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, storage.ErrWakeFeedbackExpired) {
			writeExpired()
			return
		}
		if err != nil {
			jsonError(w, "failed to save wake feedback answer", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, clientapi.PromptAnswerResponse{
			Status:      res.Status,
			Answer:      res.Response,
			AnsweredAt:  res.AnsweredAt,
			Timing:      storage.CheckinTimingOnTime,
			UnreadCount: unreadPtr(),
		})

	case storage.InboxKindPromptContext:
		if err := storage.ValidateContextPromptCategory(req.Answer); err != nil {
			jsonError(w, "invalid answer", http.StatusBadRequest)
			return
		}
		res, err := db.AnswerContextPromptByInboxID(r.Context(), promptID, req.Channel, req.Answer, now)
		if errors.Is(err, storage.ErrCheckinPromptNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, storage.ErrContextPromptExpired) {
			writeExpired()
			return
		}
		if err != nil {
			jsonError(w, "failed to save context prompt answer", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, clientapi.PromptAnswerResponse{
			Status:      res.Status,
			Answer:      res.Category,
			AnsweredAt:  res.AnsweredAt,
			Timing:      storage.CheckinTimingOnTime,
			UnreadCount: unreadPtr(),
		})
	}
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
