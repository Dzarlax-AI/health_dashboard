package api

import (
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func maintenanceSchemas() map[string]any {
	return map[string]any{
		"CacheStatus":                       storage.CacheStatus{},
		"StressObservability":               storage.StressObservabilitySummary{},
		"StressValidation":                  health.ValidationReport{},
		"CheckinCoverage":                   storage.CheckinCoverage{},
		"ImportStatus":                      ImportStatus{},
		"EnergyBackfillStatus":              EnergyBackfillStatus{},
		"EnergyBackfillSummary":             EnergyBackfillSummary{},
		"AdminAISettingsRequest":            AdminAISettingsRequest{},
		"TodayInsightsB1QualityGateRequest": TodayInsightsB1QualityGateRequest{},
		"StatusMessage":                     StatusMessage{},
		"DateBoundsRequest":                 DateBoundsRequest{},
		"CacheBackfillResponse":             CacheBackfillResponse{},
		"MetricGapsResponse":                MetricGapsResponse{},
		"QualityAuditResponse":              QualityAuditResponse{},
		"QualityFixResponse":                QualityFixResponse{},
		"QualityDigestResponse":             QualityDigestResponse{},
		"CoverageUpdateRequest":             CoverageUpdateRequest{},
		"WebhookStatusResponse":             WebhookStatusResponse{},
		"WebhookRetryResponse":              WebhookRetryResponse{},
		"AIModelsRequest":                   AIModelsRequest{},
		"AIModelsResponse":                  AIModelsResponse{},
		"AdminAISettingsResponse":           AdminAISettingsResponse{},
		"AdminEnergySettingsResponse":       AdminEnergySettingsResponse{},
		"TodayInsightsSettings":             TodayInsightsSettings{},
		"QualityGateResponse":               QualityGateResponse{},
		"AdminUsersResponse":                AdminUsersResponse{},
		"AdminNewUserResponse":              AdminNewUserResponse{},
		"AdminUserKey":                      AdminUserKey{},
		"AdminCreateUser":                   AdminCreateUser{},
		"ChronicLoadSettings":               ChronicLoadSettings{},
		"ReadinessBackfillResponse":         ReadinessBackfillResponse{},
		"ChipCalibrationResponse":           ChipCalibrationResponse{},
		"OperationalReport":                 OperationalReport{},
		"MonitoringReport":                  MonitoringReport{},
		"EnergySettingsRequest":             map[string]string{},
		"TodayInsightsSettingsRequest":      map[string]bool{},
		"ChronicLoadSettingsRequest":        map[string]int{},
	}
}

func configureMaintenanceInputs(schemas map[string]any) {
	// These handlers decode partial maps. List the keys that change state.
	// Energy ignores extra string keys. The two other handlers reject them.
	configure := func(name string, keys []string, value map[string]any, strict bool) {
		schema := schemas[name].(map[string]any)
		properties := map[string]any{}
		for _, key := range keys {
			properties[key] = value
		}
		schema["properties"] = properties
		if strict {
			schema["additionalProperties"] = false
			schema["minProperties"] = 1
		}
	}
	configure("EnergySettingsRequest", []string{"energy.beta", "energy.z_threshold", "energy.stress_drain_enabled"}, map[string]any{"type": "string"}, false)
	configure("ChronicLoadSettingsRequest", []string{storage.SettingChronicLoadMinAcuteDensity, storage.SettingChronicLoadMinBreachDays}, map[string]any{"type": "integer", "minimum": 1}, true)
	configure("TodayInsightsSettingsRequest", []string{storage.SettingTodayInsightsB0Enabled, storage.SettingTodayInsightsB1Enabled, storage.SettingTodayInsightsB1PreviewEnabled}, map[string]any{"type": "boolean"}, true)
}

func addMaintenanceOperations(paths map[string]any) {
	add := func(method, path, id, summary, request, response string, params ...any) map[string]any {
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		op := getOperation(id, summary, params, jsonResponseRef(response))
		if request != "" {
			op["requestBody"] = jsonRequestBody(request)
		}
		item[method] = op
		return op
	}
	admin := func(method, path, id, summary, request, response string, params ...any) map[string]any {
		op := add(method, path, id, summary, request, response, params...)
		op["x-required-role"] = "admin"
		op["responses"].(map[string]any)["403"] = plainTextResponse("Administrator authorization is required.")
		return op
	}
	// Cross-profile selection is privileged. Only handlers which resolve an
	// explicit administrator scope receive this parameter.
	scopedAdmin := func(method, path, id, summary, request, response string, params ...any) map[string]any {
		selector := stringQueryParameter("schema", "Optional administrator target, validated against the tenant registry. Defaults to the authenticated profile.")
		params = append(params, selector)
		return admin(method, path, id, summary, request, response, params...)
	}
	kind := enumQueryParameter("kind", "morning", "morning", "evening")
	add("post", "/api/settings/test-notify", "sendTestNotification", "Send a test Telegram notification", "", "StatusMessage", kind)
	add("get", "/api/webhook-status", "getWebhookStatus", "Read Telegram webhook registration status", "", "WebhookStatusResponse")
	scopedAdmin("post", "/api/webhook-status/retry", "retryWebhookRegistration", "Schedule another webhook registration attempt", "", "WebhookRetryResponse")

	upload := add("post", "/api/import/upload", "uploadHealthExport", "Start a streamed Apple Health import", "", "StatusMessage",
		stringQueryParameter("filename", "File name hint for detecting XML and ZIP."),
		stringQueryParameter("size", "Optional client-reported byte count for progress."),
		stringQueryParameter("batch", "Compatibility parameter for batch size."),
		stringQueryParameter("pause", "Compatibility parameter for batch pause."))
	upload["requestBody"] = map[string]any{"required": true, "description": "Raw file contents, up to 2 GiB.", "content": map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	upload["responses"].(map[string]any)["413"] = plainTextResponse("Upload exceeds 2 GiB.")
	add("get", "/api/import/status", "getImportStatus", "Read progress of the most recent import", "", "ImportStatus")
	run := add("post", "/api/settings/energy-backfill", "startEnergyBackfill", "Start retrospective Energy calculation", "DateBoundsRequest", "EnergyBackfillStatus")
	run["requestBody"].(map[string]any)["required"] = false
	run["responses"].(map[string]any)["409"] = jsonResponseRef("EnergyBackfillStatus")
	run["responses"].(map[string]any)["412"] = plainTextResponse("A timezone and eligible daily inputs are required.")
	add("get", "/api/settings/energy-backfill/status", "getEnergyBackfillStatus", "Read Energy backfill progress", "", "EnergyBackfillStatus")
	add("get", "/api/settings/energy-backfill/summary", "getEnergyBackfillSummary", "Read eligible dates for Energy backfill", "", "EnergyBackfillSummary")

	force := enumQueryParameter("force", "0", "0", "1")
	scopedAdmin("get", "/api/admin/status", "getCacheStatus", "Read aggregate cache status", "", "CacheStatus")
	scopedAdmin("post", "/api/admin/backfill", "startCacheBackfill", "Schedule a tenant cache backfill", "", "CacheBackfillResponse", force)
	scopedAdmin("get", "/api/admin/gaps", "getMetricGaps", "Read detected gaps in metric coverage", "", "MetricGapsResponse")
	scopedAdmin("get", "/api/admin/quality-audit", "getQualityAudit", "Read data quality findings", "", "QualityAuditResponse")
	scopedAdmin("post", "/api/admin/quality-fix", "applyQualityFix", "Flag impossible and suspect measurements", "", "QualityFixResponse")
	scopedAdmin("post", "/api/admin/quality-digest", "sendQualityDigest", "Send the weekly data quality digest", "", "QualityDigestResponse")
	days := integerQueryParameter("days", 14, 1, 90)
	scopedAdmin("get", "/api/admin/checkin-coverage", "getCheckinCoverage", "Read Telegram check-in coverage", "", "CheckinCoverage", days)
	scopedAdmin("post", "/api/admin/checkin-coverage", "updateCheckinCoverage", "Update check-in coverage start date", "CoverageUpdateRequest", "CheckinCoverage", days)
	admin("get", "/api/admin/settings", "getAISettings", "Read installation AI configuration", "", "AdminAISettingsResponse")
	admin("post", "/api/admin/settings", "updateAISettings", "Update installation AI configuration", "AdminAISettingsRequest", "SettingsUpdateResponse")
	admin("post", "/api/admin/ai-models", "listProviderModels", "List available models for an AI provider", "AIModelsRequest", "AIModelsResponse")
	scopedAdmin("get", "/api/admin/energy-settings", "getEnergySettings", "Read tenant Energy configuration", "", "AdminEnergySettingsResponse")
	scopedAdmin("post", "/api/admin/energy-settings", "updateEnergySettings", "Update tenant Energy configuration", "EnergySettingsRequest", "SettingsUpdateResponse")
	scopedAdmin("get", "/api/admin/today-insights/config", "getTodayInsightsConfig", "Read Today Insights configuration", "", "TodayInsightsSettings")
	scopedAdmin("post", "/api/admin/today-insights/config", "updateTodayInsightsConfig", "Update Today Insights configuration", "TodayInsightsSettingsRequest", "TodayInsightsSettings")
	scopedAdmin("post", "/api/admin/today-insights/b1-quality-gate", "recordTodayInsightsQualityGate", "Validate the frozen corpus and record its review", "TodayInsightsB1QualityGateRequest", "QualityGateResponse")
	window := integerQueryParameter("window", 30, 7, 90)
	asOf := stringQueryParameter("as_of", "Last tenant-local date in YYYY-MM-DD.")
	scopedAdmin("get", "/api/admin/stress-observability", "getStressObservability", "Read stress signal coverage and diagnostics", "", "StressObservability", window, asOf)
	scopedAdmin("get", "/api/admin/stress-validation", "getStressValidation", "Read stress validation against check-ins", "", "StressValidation", window, asOf)
	admin("get", "/api/admin/users", "listUsers", "List profiles with masked API keys", "", "AdminUsersResponse")
	admin("post", "/api/admin/users", "createUser", "Provision a profile", "AdminCreateUser", "AdminNewUserResponse")
	username := map[string]any{"name": "username", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}
	admin("get", "/api/admin/users/{username}/api-key", "getUserAPIKey", "Read the API key for a profile", "", "AdminUserKey", username)
	const readiness = "/api/admin/readiness-redesign/"
	scopedAdmin("post", readiness+"backfill", "backfillReadiness", "Rebuild readiness snapshots over an inclusive date range", "", "ReadinessBackfillResponse", requiredStringQueryParameter("from", "First date in YYYY-MM-DD."), requiredStringQueryParameter("to", "Last date in YYYY-MM-DD."), stringQueryParameter("sub_score", "Sub-score name or all."), force)
	scopedAdmin("get", readiness+"config", "getReadinessConfig", "Read chronic load calibration thresholds", "", "ChronicLoadSettings")
	scopedAdmin("post", readiness+"config", "updateReadinessConfig", "Update chronic load calibration thresholds", "ChronicLoadSettingsRequest", "ChronicLoadSettings")
	scopedAdmin("get", readiness+"chip-calibrations", "listChipCalibrations", "Read readiness chip calibrations", "", "ChipCalibrationResponse")
	scopedAdmin("post", readiness+"chip-calibrations", "recomputeChipCalibrations", "Recompute chip calibrations for a single tenant", "", "ChipCalibrationResponse")
	scopedAdmin("get", readiness+"operational-contract", "getReadinessOperationalContract", "Read the readiness operational contract", "", "OperationalReport", days)
	scopedAdmin("get", readiness+"monitoring", "getReadinessMonitoring", "Read readiness monitoring summaries", "", "MonitoringReport")
}
