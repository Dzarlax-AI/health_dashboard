package notify

import (
	"fmt"
	"html"
	"strings"
	"time"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func formatMorningRich(b *health.BriefingResponse, aiBlocks map[string]string, lang string, loc *time.Location, f freshness, checkinExpired bool, settleBanner string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<h2>🌅 %s — %s</h2>\n", richEsc(tr(lang, "tg_morning_header")), richEsc(b.Date))

	if settleBanner != "" {
		richTrustedParagraph(&sb, settleBanner)
	}
	if d := staleDays(b.Date, loc); d >= 1 {
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_warn_stale"), d))
	}

	renderRichHeadline(&sb, b.Headline)
	renderRichMorningSummary(&sb, b, f, lang)

	ai := struct{ Sleep, Yesterday, Recovery, Recommendation string }{
		Sleep:          aiBlocks["SLEEP"],
		Yesterday:      aiBlocks["YESTERDAY"],
		Recovery:       aiBlocks["RECOVERY"],
		Recommendation: aiBlocks["RECOMMENDATION"],
	}

	renderRichEnergyBank(&sb, b.EnergyBank, lang)
	renderRichReadiness(&sb, b, lang)
	renderRichAlerts(&sb, b.Alerts, lang)
	renderRichContextAnnotations(&sb, b.ContextAnnotations, lang)

	switch {
	case f.sleepStale() && f.sleepKnown:
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_sleep_silence"), fmtSilence(f.sleep, lang)))
	case b.Sleep == nil:
		richTrustedParagraph(&sb, tr(lang, "tg_warn_no_sleep"))
	default:
		renderRichSection(&sb, findSection(b, "sleep"))
		renderRichAITake(&sb, ai.Sleep)
		renderRichSleepSources(&sb, b.Sleep, lang)
	}

	if f.phoneOff() && f.phoneKnown {
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_phone_off"), fmtSilence(f.phone, lang)))
	} else {
		actSec := findSection(b, "activity")
		cardioSec := findSection(b, "cardio")
		if actSec != nil || cardioSec != nil || ai.Yesterday != "" {
			fmt.Fprintf(&sb, "<h3>📅 %s</h3>\n", richEsc(tr(lang, "tg_yesterday")))
			renderRichSectionDetails(&sb, actSec)
			renderRichSectionDetails(&sb, cardioSec)
			renderRichAITake(&sb, ai.Yesterday)
		}
	}

	if f.watchOff() && f.watchKnown {
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_watch_off"), fmtSilence(f.watch, lang)))
	} else if recSec := findSection(b, "recovery"); recSec != nil {
		renderRichSection(&sb, recSec)
		renderRichAITake(&sb, ai.Recovery)
	}

	if ai.Recommendation != "" {
		fmt.Fprintf(&sb, "<h3>🎯 %s</h3>\n<p>%s</p>\n", richEsc(tr(lang, "tg_recommendation")), richText(ai.Recommendation))
	}
	renderRichFreshnessDetails(&sb, f, lang)

	if checkinExpired {
		if note := tr(lang, "checkin_expired_note"); note != "" && note != "checkin_expired_note" {
			richTrustedParagraph(&sb, note)
		}
	}
	return strings.TrimSpace(sb.String())
}

func formatEveningRich(b *health.BriefingResponse, dash *storage.DashboardResponse, lang string, loc *time.Location, f freshness) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<h2>🌆 %s — %s</h2>\n", richEsc(tr(lang, "tg_evening_header")), richEsc(b.Date))

	if d := staleDays(b.Date, loc); d >= 1 {
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_warn_stale"), d))
	}

	now := time.Now().In(loc)
	today := fmt.Sprintf("%d-%02d-%02d", now.Year(), int(now.Month()), now.Day())
	hasDash := dash != nil && dash.Date == today && len(dash.Cards) > 0

	switch {
	case f.phoneOff() && f.phoneKnown:
		richTrustedParagraph(&sb, fmt.Sprintf(tr(lang, "tg_phone_off"), fmtSilence(f.phone, lang)))
	case !hasDash:
		richTrustedParagraph(&sb, tr(lang, "tg_warn_no_activity"))
	default:
		renderRichTodayTable(&sb, dash, lang)
	}

	renderRichEnergyBank(&sb, b.EnergyBank, lang)

	fmt.Fprintf(&sb, "<h3>%s %s</h3>\n<p><strong>%d/100</strong> — %s</p>\n",
		richEsc(readinessEmoji(b.ReadinessScore)), richEsc(tr(lang, "tg_readiness")),
		b.ReadinessScore, richText(b.ReadinessLabel))

	renderRichAlerts(&sb, b.Alerts, lang)
	if len(b.Insights) > 0 {
		fmt.Fprintf(&sb, "<h3>💡 %s</h3>\n<ul>\n", richEsc(tr(lang, "tg_insights")))
		for i, ins := range b.Insights {
			if i >= 3 {
				break
			}
			icon := "✅"
			if ins.Type == "warning" {
				icon = "⚠️"
			}
			fmt.Fprintf(&sb, "<li>%s %s</li>\n", richEsc(icon), richText(ins.Text))
		}
		sb.WriteString("</ul>\n")
	}
	renderRichFreshnessDetails(&sb, f, lang)
	return strings.TrimSpace(sb.String())
}

func richEsc(s string) string {
	return html.EscapeString(s)
}

func richText(s string) string {
	escaped := html.EscapeString(strings.TrimSpace(s))
	return strings.ReplaceAll(escaped, "\n", "<br>")
}

func richTrustedParagraph(sb *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(sb, "<p>%s</p>\n", body)
}

func renderRichHeadline(sb *strings.Builder, h *health.HeadlineSignal) {
	if h == nil || h.Title == "" {
		return
	}
	fmt.Fprintf(sb, "<p><strong>%s %s</strong>", richEsc(headlineEmoji(h.Severity)), richText(h.Title))
	if h.Detail != "" {
		fmt.Fprintf(sb, "<br><em>%s</em>", richText(h.Detail))
	}
	sb.WriteString("</p>\n")
}

func renderRichContextAnnotations(sb *strings.Builder, annotations []health.ContextAnnotationSummary, lang string) {
	if len(annotations) == 0 {
		return
	}
	fmt.Fprintf(sb, "<h3>📝 %s</h3>\n<ul>\n", richEsc(tr(lang, "tg_context_notes")))
	for _, a := range annotations {
		label := tr(lang, "context_prompt_label_"+a.Category)
		if label == "context_prompt_label_"+a.Category {
			label = tr(lang, "context_prompt_label_unknown_context")
		}
		fmt.Fprintf(sb, "<li>%s</li>\n", richText(label))
	}
	sb.WriteString("</ul>\n")
}

func renderRichMorningSummary(sb *strings.Builder, b *health.BriefingResponse, f freshness, lang string) {
	sleepValue := "—"
	sleepRead := tr(lang, "tg_warn_no_sleep")
	if f.sleepStale() && f.sleepKnown {
		sleepRead = fmt.Sprintf(tr(lang, "tg_sleep_silence"), fmtSilence(f.sleep, lang))
	} else if b.Sleep != nil {
		sleepValue = fmt.Sprintf("%.1fh", b.Sleep.TotalAvg)
		sleepRead = sectionSummary(findSection(b, "sleep"))
	}
	recoveryValue := fmt.Sprintf("%d%%", b.RecoveryPct)
	recoveryRead := sectionSummary(findSection(b, "recovery"))
	if f.watchOff() && f.watchKnown {
		recoveryValue = "—"
		recoveryRead = fmt.Sprintf(tr(lang, "tg_watch_off"), fmtSilence(f.watch, lang))
	}
	energyValue, energyRead := "—", "—"
	if b.EnergyBank != nil {
		energyValue = fmt.Sprintf("%d/%d", b.EnergyBank.Current, b.EnergyBank.Capacity)
		energyRead = energyVerdictLabel(b.EnergyBank, lang)
	}

	sb.WriteString("<table>\n<tr><th>Signal</th><th>Now</th><th>Read</th></tr>\n")
	fmt.Fprintf(sb, "<tr><td>⚡ %s</td><td>%s</td><td>%s</td></tr>\n", richEsc(tr(lang, "tg_energy")), richEsc(energyValue), richText(energyRead))
	fmt.Fprintf(sb, "<tr><td>%s %s</td><td>%d/100</td><td>%s</td></tr>\n", richEsc(readinessEmoji(b.ReadinessToday)), richEsc(tr(lang, "tg_readiness")), b.ReadinessToday, richText(b.ReadinessTodayLabel))
	fmt.Fprintf(sb, "<tr><td>😴 Sleep</td><td>%s</td><td>%s</td></tr>\n", richEsc(sleepValue), richText(stripSimpleTags(sleepRead)))
	fmt.Fprintf(sb, "<tr><td>❤️ Recovery</td><td>%s</td><td>%s</td></tr>\n", richEsc(recoveryValue), richText(stripSimpleTags(recoveryRead)))
	sb.WriteString("</table>\n")
}

func renderRichTodayTable(sb *strings.Builder, dash *storage.DashboardResponse, lang string) {
	fmt.Fprintf(sb, "<h3>📊 %s</h3>\n", richEsc(tr(lang, "tg_today")))
	dashMap := make(map[string]storage.CardData, len(dash.Cards))
	for _, c := range dash.Cards {
		dashMap[c.Metric] = c
	}
	icons := map[string]string{
		"step_count":          "👟",
		"active_energy":       "🔥",
		"apple_exercise_time": "🏃",
	}
	sb.WriteString("<table>\n<tr><th>Metric</th><th>Value</th><th>Vs yesterday</th></tr>\n")
	for _, metric := range []string{"step_count", "active_energy", "apple_exercise_time"} {
		c, ok := dashMap[metric]
		if !ok || c.Value <= 0 {
			continue
		}
		trend := "—"
		if c.Prev > 0 {
			pct := (c.Value - c.Prev) / c.Prev * 100
			switch {
			case pct > 5:
				trend = fmt.Sprintf(tr(lang, "tg_vs_yesterday_up"), pct)
			case pct < -5:
				trend = fmt.Sprintf(tr(lang, "tg_vs_yesterday_down"), pct)
			}
		}
		fmt.Fprintf(sb, "<tr><td>%s %s</td><td>%.0f %s</td><td>%s</td></tr>\n",
			richEsc(icons[metric]), richEsc(prettyMetric(metric)), c.Value, richEsc(c.Unit), richText(trend))
	}
	sb.WriteString("</table>\n")
}

func renderRichSection(sb *strings.Builder, sec *health.BriefingSection) {
	if sec == nil {
		return
	}
	fmt.Fprintf(sb, "<h3>%s %s</h3>\n<p>%s</p>\n", richEsc(statusEmoji[sec.Status]), richText(sec.Title), richText(sec.Summary))
	renderRichSectionDetails(sb, sec)
}

func renderRichSectionDetails(sb *strings.Builder, sec *health.BriefingSection) {
	if sec == nil || len(sec.Details) == 0 {
		return
	}
	sb.WriteString("<ul>\n")
	for _, d := range sec.Details {
		fmt.Fprintf(sb, "<li><strong>%s:</strong> %s", richText(d.Label), richText(d.Value))
		if d.Note != "" && !isBoringNote(d.Note) {
			fmt.Fprintf(sb, " <em>(%s)</em>", richText(d.Note))
		}
		sb.WriteString("</li>\n")
	}
	sb.WriteString("</ul>\n")
}

func renderRichAITake(sb *strings.Builder, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	fmt.Fprintf(sb, "<blockquote>🤖 %s</blockquote>\n", richText(body))
}

func renderRichEnergyBank(sb *strings.Builder, eb *health.EnergyBank, lang string) {
	if eb == nil || eb.Capacity == 0 {
		return
	}
	fmt.Fprintf(sb, "<h3>⚡ %s</h3>\n<p><strong>%d/%d — %s</strong>",
		richEsc(tr(lang, "tg_energy")), eb.Current, eb.Capacity, richText(energyVerdictLabel(eb, lang)))
	if eb.VerdictReason != "" {
		fmt.Fprintf(sb, "<br><em>%s</em>", richText(eb.VerdictReason))
	}
	sb.WriteString("</p>\n")
}

func renderRichReadiness(sb *strings.Builder, b *health.BriefingResponse, lang string) {
	today := b.ReadinessToday
	trend := b.ReadinessScore
	fmt.Fprintf(sb, "<h3>%s %s</h3>\n", richEsc(readinessEmoji(today)), richEsc(tr(lang, "tg_readiness")))
	if abs(today-trend) <= 2 {
		fmt.Fprintf(sb, "<p><strong>%d/100</strong> — %s</p>\n", today, richText(b.ReadinessTodayLabel))
	} else {
		fmt.Fprintf(sb, "<p><strong>%s: %d/100</strong> — %s<br>%s: %d/100 — %s</p>\n",
			richEsc(tr(lang, "tg_readiness_today")), today, richText(b.ReadinessTodayLabel),
			richEsc(tr(lang, "tg_readiness_trend")), trend, richText(b.ReadinessLabel))
	}
	if b.ReadinessTip != "" {
		fmt.Fprintf(sb, "<p><em>%s</em></p>\n", richText(b.ReadinessTip))
	}
}

func renderRichAlerts(sb *strings.Builder, alerts []health.Alert, lang string) {
	if len(alerts) == 0 {
		return
	}
	fmt.Fprintf(sb, "<h3>⚠️ %s</h3>\n<ul>\n", richEsc(tr(lang, "tg_alerts")))
	for _, a := range alerts {
		fmt.Fprintf(sb, "<li>%s %s</li>\n", richEsc(alertEmoji(a.Severity)), richText(a.Text))
	}
	sb.WriteString("</ul>\n")
}

func renderRichSleepSources(sb *strings.Builder, sleep *health.SleepAnalysis, lang string) {
	if sleep == nil || len(sleep.Sources) <= 1 {
		return
	}
	fmt.Fprintf(sb, "<details><summary>%s</summary>\n<ul>\n", richEsc(tr(lang, "tg_sources")))
	for _, src := range sleep.Sources {
		fmt.Fprintf(sb, "<li>%s — %.1fh</li>\n", richText(src.Source), src.Total)
	}
	sb.WriteString("</ul>\n</details>\n")
}

func renderRichFreshnessDetails(sb *strings.Builder, f freshness, lang string) {
	if !f.sleepKnown && !f.watchKnown && !f.phoneKnown {
		return
	}
	sb.WriteString("<details><summary>Freshness</summary>\n<ul>\n")
	if f.sleepKnown {
		fmt.Fprintf(sb, "<li>Sleep — %s</li>\n", richEsc(fmtSilence(f.sleep, lang)))
	}
	if f.watchKnown {
		fmt.Fprintf(sb, "<li>Watch — %s</li>\n", richEsc(fmtSilence(f.watch, lang)))
	}
	if f.phoneKnown {
		fmt.Fprintf(sb, "<li>Activity — %s</li>\n", richEsc(fmtSilence(f.phone, lang)))
	}
	sb.WriteString("</ul>\n</details>\n")
}

func sectionSummary(sec *health.BriefingSection) string {
	if sec == nil || sec.Summary == "" {
		return "—"
	}
	return sec.Summary
}

func energyVerdictLabel(eb *health.EnergyBank, lang string) string {
	if eb == nil {
		return "—"
	}
	if eb.VerdictLabel != "" {
		return eb.VerdictLabel
	}
	verdict := tr(lang, "energy_verdict_"+eb.ActionVerdict)
	if verdict == "energy_verdict_"+eb.ActionVerdict {
		return eb.ActionVerdict
	}
	return verdict
}

func stripSimpleTags(s string) string {
	replacer := strings.NewReplacer(
		"<i>", "", "</i>", "",
		"<b>", "", "</b>", "",
	)
	return replacer.Replace(s)
}
