package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"unicode/utf8"

	"health-receiver/internal/health"
)

func TestAIInsightReviewFingerprintBindsReaderCopyValidationRevision(t *testing.T) {
	withoutValidationRevision, err := json.Marshal(struct {
		Prompt         string          `json:"prompt"`
		PromptRevision string          `json:"prompt_revision"`
		Schema         *ResponseSchema `json:"schema"`
		SafetyPrompt   string          `json:"safety_prompt"`
		SafetyRevision string          `json:"safety_revision"`
		SafetySchema   *ResponseSchema `json:"safety_schema"`
		InputVersion   string          `json:"input_version"`
	}{
		Prompt:         aiInsightSystemPrompt,
		PromptRevision: AIInsightPromptRevision,
		Schema:         aiInsightResponseSchema,
		SafetyPrompt:   aiInsightSafetyPrompt,
		SafetyRevision: AIInsightSafetyPromptRevision,
		SafetySchema:   aiInsightSafetySchema,
		InputVersion:   health.AIInsightInputVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutSum := sha256.Sum256(withoutValidationRevision)
	if got, without := AIInsightReviewFingerprint(), hex.EncodeToString(withoutSum[:]); got == without {
		t.Fatal("review fingerprint does not bind the reader-copy validation revision")
	}
}

func testAIInsightInput(t *testing.T) health.AIInsightInput {
	t.Helper()
	snapshot := dailyInsightTestSnapshot(t)
	input, ok := health.BuildAIInsightInput(snapshot, "en", "overall", nil)
	if !ok {
		t.Fatal("test snapshot has no eligible overall insight")
	}
	return input
}

func TestGenerateAIInsightAllowsReviewedDisagreement(t *testing.T) {
	input := testAIInsightInput(t)
	provider := &dailyInsightTestProvider{responses: []string{
		fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":{"text":"I read the short sleep as one signal, not a command to stop ordinary plans.","fact_ids":[%q],"stance":"disagree","alternative_action":"Choose a familiar easy walk."}}`, health.AIInsightVersion, input.Facts[0].ID),
		fmt.Sprintf(`{"version":%q,"verdict":"allow","categories":[],"language_review":{"text":{"reader_gender":"neutral","serbian_ai_voice":"not_applicable","evidence":""},"alternative_action":{"reader_gender":"neutral","serbian_ai_voice":"not_applicable","evidence":""}}}`, aiInsightSafetyVersion),
	}}
	result, err := GenerateAIInsightSlot(context.Background(), provider, ProviderConfig{}, input)
	if err != nil || result.Insight == nil || result.Insight.Stance != "disagree" || result.Review == nil || result.Review.Verdict != "allow" || len(provider.requests) != 2 {
		t.Fatalf("result=%#v err=%v requests=%d", result, err, len(provider.requests))
	}
	if result.InputTokens != 100 || result.ReviewUsage.InputTokens != 101 || result.ReviewUsage.OutputTokens != 11 || result.ReviewUsage.Attempts != 1 || result.ReviewUsage.Latency.Milliseconds() != 2 {
		t.Fatalf("author/review usage not separated: %#v", result)
	}
}

func TestGenerateAIInsightRejectsUnsupportedDisagreement(t *testing.T) {
	input := testAIInsightInput(t)
	provider := &dailyInsightTestProvider{responses: []string{
		fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":{"text":"I disagree with the server.","fact_ids":[%q],"stance":"disagree","alternative_action":""}}`, health.AIInsightVersion, input.Facts[0].ID),
		fmt.Sprintf(`{"version":%q,"verdict":"reject","categories":["unsupported_disagreement"]}`, aiInsightSafetyVersion),
	}}
	result, err := GenerateAIInsightSlot(context.Background(), provider, ProviderConfig{}, input)
	if err == nil || result.Insight != nil || result.Review == nil || result.Review.Categories[0] != "unsupported_disagreement" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestGenerateAIInsightRejectsReaderArtifactBeforeReview(t *testing.T) {
	input := testAIInsightInput(t)
	provider := &dailyInsightTestProvider{responses: []string{
		fmt.Sprintf(`{"version":%q,"locale":"en","slot":"overall","insight":{"text":"Keep the day easy. [Correction: rewrite this.]","fact_ids":[%q],"stance":"qualify","alternative_action":""}}`, health.AIInsightVersion, input.Facts[0].ID),
	}}
	result, err := GenerateAIInsightSlot(context.Background(), provider, ProviderConfig{}, input)
	if err == nil || result.Insight != nil || len(provider.requests) != 1 || result.Text != "" {
		t.Fatalf("reader artifact was not rejected before review: result=%#v err=%v requests=%d", result, err, len(provider.requests))
	}
}

// These tests exercise the review contract, not a model's ability to classify
// grammar. The frozen sentinel still needs a fresh, manually read provider run.
func TestAIInsightFieldLanguageReviewRejectsSentinelFailures(t *testing.T) {
	cases := []struct{ name, locale, field, text, quote string }{
		{"060-r1-overall", "sr", "text", "Umeren dan deluje razumno, ali „normalan trening je u redu“ zvuči sigurnije nego što podaci potvrđuju: spremnost je umerena, a EnergyBank je označen kao preliminaran. To ne traži da preskočiš aktivnost, samo je ne bih uzimao kao signal da danas pojačaš napor.", "bih uzimao"},
		{"060-r2-overall", "sr", "text", "Spremnost je umerena, a HRV i puls u mirovanju prikazani su kao današnja merenja, bez ličnog poređenja koje bi pokazalo da su neuobičajeno dobri. Zato bih ograničio tvrdnju da je normalan trening sigurno u redu. Uz to, podaci o energiji su označeni kao delimični i privremeni; trend koraka je blago viši nego u prethodnih pet dana, ali sam po sebi ne govori da treba pojačati trening. Za danas ima smisla ostati pri umerenom naporu.", "bih ograničio"},
		{"017-r1-overall", "ru", "alternative_action", "Если планировал тренировку, замени интенсивную часть на спокойную прогулку или обычные дела без дополнительной нагрузки.", "планировал"},
		{"017-r2-energy", "ru", "alternative_action", "Если планировал тренировку, выбери спокойную прогулку или отдых.", "планировал"},
		{"017-r3-energy", "ru", "alternative_action", "Если планировал тренировку, выбери сегодня спокойную прогулку или отдых вместо интенсивной нагрузки.", "планировал"},
		{"050-r3-recovery", "ru", "alternative_action", "Если планировал тренировку, оставь её в привычном объёме и не повышай нагрузку специально.", "планировал"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Also swap fields: neither field may bypass the same grammar gate.
			for _, field := range []string{tc.field, otherAIInsightField(tc.field)} {
				language := neutralAIInsightLanguageReview(tc.locale)
				assessment := language[field].(map[string]any)
				assessment["evidence"] = tc.quote
				if tc.locale == "sr" {
					assessment["serbian_ai_voice"] = "gender_marked"
				} else {
					assessment["reader_gender"] = "assumed"
				}
				text := tc.text
				if field == "alternative_action" && utf8.RuneCountInString(text) > 300 {
					text = tc.quote + "."
				}
				result, err, provider := generateWithLanguageReview(t, tc.locale, field, text, language)
				if err == nil || result.Insight != nil || len(provider.requests) != 2 || result.Review == nil || result.Review.Verdict != "reject" {
					t.Fatalf("%s accepted a marked field despite its language review", field)
				}
			}
		})
	}
}

func otherAIInsightField(field string) string {
	if field == "text" {
		return "alternative_action"
	}
	return "text"
}

func neutralAIInsightLanguageReview(locale string) map[string]any {
	voice := "not_applicable"
	if locale == "sr" {
		voice = "neutral"
	}
	return map[string]any{
		"text":               map[string]any{"reader_gender": "neutral", "serbian_ai_voice": voice, "evidence": ""},
		"alternative_action": map[string]any{"reader_gender": "neutral", "serbian_ai_voice": voice, "evidence": ""},
	}
}

func generateWithLanguageReview(t *testing.T, locale, field, text string, language any) (AIInsightSlotResult, error, *dailyInsightTestProvider) {
	t.Helper()
	input := testAIInsightInput(t)
	input.Locale = locale
	section := map[string]any{"text": "Podaci ukazuju na mirniji tempo.", "alternative_action": "", "stance": "qualify", "fact_ids": []string{input.Facts[0].ID}}
	if locale == "ru" {
		section["text"] = "Можно выбрать спокойный темп."
	}
	section[field] = text
	candidate, err := json.Marshal(map[string]any{"version": health.AIInsightVersion, "locale": locale, "slot": input.Slot, "insight": section})
	if err != nil {
		t.Fatal(err)
	}
	review, err := json.Marshal(map[string]any{"version": aiInsightSafetyVersion, "verdict": "allow", "categories": []string{}, "language_review": language})
	if err != nil {
		t.Fatal(err)
	}
	provider := &dailyInsightTestProvider{responses: []string{string(candidate), string(review)}}
	result, err := GenerateAIInsightSlot(context.Background(), provider, ProviderConfig{}, input)
	return result, err, provider
}

func TestAIInsightLanguageReviewIsRequiredAndFieldBound(t *testing.T) {
	for _, mutation := range []string{"missing", "missing-action", "missing-reader", "missing-evidence", "unknown-reader", "wrong-locale", "invented-evidence", "cross-field-evidence"} {
		t.Run(mutation, func(t *testing.T) {
			language := neutralAIInsightLanguageReview("ru")
			action := language["alternative_action"].(map[string]any)
			switch mutation {
			case "missing":
				language = nil
			case "missing-action":
				delete(language, "alternative_action")
			case "missing-reader":
				delete(action, "reader_gender")
			case "missing-evidence":
				delete(action, "evidence")
			case "unknown-reader":
				action["reader_gender"] = "maybe"
			case "wrong-locale":
				action["serbian_ai_voice"] = "neutral"
			case "invented-evidence":
				action["evidence"] = "несуществующая фраза"
			case "cross-field-evidence":
				action["reader_gender"] = "assumed"
				action["evidence"] = "Можно выбрать спокойный темп."
			}
			result, err, _ := generateWithLanguageReview(t, "ru", "alternative_action", "Выбери комфортный темп.", language)
			if err == nil || result.Insight != nil {
				t.Fatal("incomplete or inconsistent language review accepted")
			}
		})
	}
}

func TestAIInsightLanguageReviewAllowsNeutralProse(t *testing.T) {
	for _, tc := range []struct{ locale, text string }{
		{"ru", "Ты видишь сигнал."}, {"ru", "Ты можешь выбрать интервал."},
		{"ru", "Если сигнал слабый, не меняй план только из-за него."},
		{"ru", "Если тренировка была в планах, выбери привычную нагрузку."},
		{"sr", "Ne bih da menjaš plan samo zbog jednog signala."},
		{"sr", "Mislim da izbor ostaje otvoren."},
		{"sr", "Signal je porastao, ali to ne menja današnji plan."},
	} {
		for _, field := range []string{"text", "alternative_action"} {
			result, err, provider := generateWithLanguageReview(t, tc.locale, field, tc.text, neutralAIInsightLanguageReview(tc.locale))
			if err != nil || result.Insight == nil || len(provider.requests) != 2 {
				t.Fatalf("neutral %s rejected: %q: %v", field, tc.text, err)
			}
		}
	}
}

func TestAIInsightLanguageReviewAcceptsNotApplicableForEmptySerbianAction(t *testing.T) {
	language := neutralAIInsightLanguageReview("sr")
	language["alternative_action"].(map[string]any)["serbian_ai_voice"] = "not_applicable"
	result, err, provider := generateWithLanguageReview(t, "sr", "text", "Danas je važan mirniji tempo.", language)
	if err != nil || result.Insight == nil || len(provider.requests) != 2 {
		t.Fatalf("empty action review was rejected: result=%#v err=%v", result, err)
	}
}
