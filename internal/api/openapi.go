package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"

	"github.com/invopop/jsonschema"
)

const (
	OpenAPIVersion  = "3.1.0"
	ContractVersion = "0.3.0"
)

// GenerateOpenAPI builds the canonical public client contract. Route metadata
// is explicit; response schemas are reflected from the exact Go values encoded
// by handlers. This keeps the contract deterministic without making handler
// registration depend on documentation code.
func GenerateOpenAPI() ([]byte, error) {
	schemas := map[string]any{}
	for name, value := range map[string]any{
		"AIBriefingResponse":        AIBriefingResponse{},
		"DashboardResponse":         storage.DashboardResponse{},
		"DerivedMetricsResponse":    DerivedMetricsResponse{},
		"EnergyHistoryDayResponse":  EnergyHistoryDayResponse{},
		"EnergyHistoryHourResponse": EnergyHistoryHourResponse{},
		"HealthBriefingResponse":    health.BriefingResponse{},
		"MetricDataResponse":        MetricDataResponse{},
		"MetricRangeResponse":       MetricRangeResponse{},
		"ReadinessHistoryResponse":  ReadinessHistoryResponse{},
		"SectionResponse":           SectionResponse{},
		"SessionResponse":           SessionResponse{},
	} {
		schema, err := reflectedSchema(value)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		schemas[name] = schema
	}
	for _, schema := range schemas {
		allowNullableArrays(schema)
	}

	// These are stable wire enums already defined by server business logic.
	// Reflection cannot infer enum values from plain Go strings.
	for _, enum := range []struct {
		schema string
		path   []string
		values []string
	}{
		{"HealthBriefingResponse", []string{"overall"}, []string{"", "good", "fair", "low"}},
		{"HealthBriefingResponse", []string{"readiness_band"}, []string{"", "optimal", "fair", "low"}},
		{"HealthBriefingResponse", []string{"readiness_today_band"}, []string{"", "optimal", "fair", "low"}},
	} {
		if err := setPropertyEnum(schemas[enum.schema], enum.path, enum.values...); err != nil {
			return nil, fmt.Errorf("schema %s: %w", enum.schema, err)
		}
	}
	for _, enum := range []struct {
		schema string
		parent string
		child  string
		values []string
	}{
		{"HealthBriefingResponse", "sections", "status", []string{"good", "fair", "low"}},
		{"HealthBriefingResponse", "energy_bank", "action_verdict", []string{"push_hard", "moderate", "active_recovery", "rest"}},
		{"HealthBriefingResponse", "readiness_serving", "status", []string{"fresh", "missing", "stale", "data_accruing", "low_coverage", "capped"}},
		{"HealthBriefingResponse", "readiness_serving", "confidence", []string{"final", "provisional", "low"}},
		{"ReadinessHistoryResponse", "points", "band", []string{"optimal", "fair", "low"}},
		{"DerivedMetricsResponse", "values", "value_type", []string{"number", "text", "timestamp", "json"}},
		{"DerivedMetricsResponse", "values", "state", []string{"provisional", "final"}},
		{"EnergyHistoryDayResponse", "points", "verdict", []string{"push_hard", "moderate", "active_recovery", "rest"}},
	} {
		if err := setNestedPropertyEnum(schemas[enum.schema], enum.parent, enum.child, enum.values...); err != nil {
			return nil, fmt.Errorf("schema %s: %w", enum.schema, err)
		}
	}
	allowPropertyNull(schemas["HealthBriefingResponse"], "sleep")

	doc := map[string]any{
		"openapi": OpenAPIVersion,
		"info": map[string]any{
			"title":       "Health Dashboard Client API",
			"version":     ContractVersion,
			"description": "Tenant-aware read API shared by the web dashboard and native health-sync client. Tenant identity is resolved only from authenticated backend context.",
		},
		"servers": []any{map[string]any{"url": "/"}},
		"security": []any{
			map[string]any{"ApiKeyAuth": []any{}},
			map[string]any{"SessionCookie": []any{}},
		},
		"paths": clientPaths(),
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"ApiKeyAuth": map[string]any{
					"type":        "apiKey",
					"in":          "header",
					"name":        "X-API-Key",
					"description": "Per-tenant API key used by native and machine clients.",
				},
				"SessionCookie": map[string]any{
					"type":        "apiKey",
					"in":          "cookie",
					"name":        "auth",
					"description": "Opaque local browser session. Authentik ForwardAuth may establish this session, but clients must not synthesize trusted proxy headers.",
				},
			},
			"schemas": schemas,
		},
		"x-compatibility-policy": map[string]any{
			"default": "additive",
			"breaking": []any{
				"remove a documented path or operation",
				"remove or redirect a documented success response schema",
				"remove a parameter or narrow its accepted values",
				"remove a required response field",
				"change a response field type",
				"remove null from an optional response field",
				"narrow a documented enum",
			},
		},
		"x-tenant-resolution": "The backend resolves tenant identity from the authenticated API key or session. Public client operations expose no tenant or schema selector.",
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAPI: %w", err)
	}
	return append(out, '\n'), nil
}

func clientPaths() map[string]any {
	energyOperation := getOperation(
		"getEnergyHistory",
		"EnergyBank day or intraday history",
		[]any{
			enumQueryParameter("granularity", "day", "day", "hour"),
			integerQueryParameter("days", 14, 1, 365),
			integerQueryParameter("hours", 72, 1, 720),
		},
		map[string]any{
			"description": "Energy history. The response schema is selected by granularity.",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"oneOf": []any{
							schemaRef("EnergyHistoryDayResponse"),
							schemaRef("EnergyHistoryHourResponse"),
						},
						"discriminator": map[string]any{
							"propertyName": "granularity",
							"mapping": map[string]any{
								"day":  "#/components/schemas/EnergyHistoryDayResponse",
								"hour": "#/components/schemas/EnergyHistoryHourResponse",
							},
						},
					},
				},
			},
		},
	)
	energyOperation["responses"].(map[string]any)["400"] = plainTextResponse(
		"Invalid granularity. Use day or hour.",
	)
	derivedMetricsOperation := getOperation(
		"getDerivedMetrics",
		"Canonical service-derived metric history",
		[]any{
			requiredEnumQueryParameter("metric", "Canonical metric name. Missing or unsupported values return 400.", "wake_time"),
			stringQueryParameter("from", "Start date in YYYY-MM-DD; defaults to 30 days before to."),
			stringQueryParameter("to", "End date in YYYY-MM-DD; defaults to tenant-local today."),
		},
		jsonResponseRef("DerivedMetricsResponse"),
	)
	derivedMetricsOperation["responses"].(map[string]any)["400"] = jsonErrorResponse(
		"Missing or unsupported metric, or an invalid date range.",
	)

	return map[string]any{
		"/api/dashboard": map[string]any{
			"get": getOperation(
				"getDashboard",
				"Lean current-day metric dashboard",
				nil,
				jsonResponseRef("DashboardResponse"),
			),
		},
		"/api/health-briefing": map[string]any{
			"get": getOperation(
				"getHealthBriefing",
				"Rich current-day health briefing",
				[]any{langParameter()},
				jsonResponseRef("HealthBriefingResponse"),
			),
		},
		"/api/ai-briefing": map[string]any{
			"get": getOperation(
				"getAIBriefing",
				"Non-blocking cached AI narrative",
				[]any{
					langParameter(),
					stringQueryParameter("date", "Optional historical date. Past dates are cache-only and never trigger AI generation."),
				},
				jsonResponseRef("AIBriefingResponse"),
			),
		},
		"/api/readiness-history": map[string]any{
			"get": getOperation(
				"getReadinessHistory",
				"Readiness score history",
				[]any{integerQueryParameter("days", 30, 1, 365)},
				jsonResponseRef("ReadinessHistoryResponse"),
			),
		},
		"/api/energy-history": map[string]any{
			"get": energyOperation,
		},
		"/api/derived-metrics": map[string]any{
			"get": derivedMetricsOperation,
		},
		"/api/metrics/data": map[string]any{
			"get": getOperation(
				"getMetricData",
				"Time-bucketed metric history",
				[]any{
					requiredStringQueryParameter("metric", "Canonical metric name."),
					stringQueryParameter("from", "Start date in YYYY-MM-DD."),
					stringQueryParameter("to", "End date in YYYY-MM-DD."),
					enumQueryParameter("bucket", "day", "minute", "hour", "day"),
					enumQueryParameter("agg", "AVG", "AVG", "SUM", "MAX", "MIN"),
					enumQueryParameter("by_source", "0", "0", "1"),
				},
				jsonResponseRef("MetricDataResponse"),
			),
		},
		"/api/metrics/range": map[string]any{
			"get": getOperation(
				"getMetricRange",
				"Earliest and latest dates for a metric",
				[]any{requiredStringQueryParameter("metric", "Canonical metric name.")},
				jsonResponseRef("MetricRangeResponse"),
			),
		},
		"/api/section/{key}": map[string]any{
			"get": getOperation(
				"getSection",
				"Localized rule-based detail section",
				[]any{
					requiredPathEnumParameter("key", "Section key.", "sleep", "cardio", "activity", "recovery"),
					langParameter(),
				},
				jsonResponseRef("SectionResponse"),
			),
		},
		"/api/session": map[string]any{
			"get": getOperation(
				"getSession",
				"Authenticated browser capabilities",
				nil,
				jsonResponseRef("SessionResponse"),
			),
		},
	}
}

func getOperation(operationID, summary string, parameters []any, okResponse map[string]any) map[string]any {
	op := map[string]any{
		"operationId": operationID,
		"summary":     summary,
		"responses": map[string]any{
			"200": okResponse,
			"302": redirectResponse("Browser authentication or initial setup is required; Location identifies the interactive route."),
			"500": plainTextResponse("The backend could not load or encode the tenant response."),
		},
	}
	if len(parameters) > 0 {
		op["parameters"] = parameters
	}
	return op
}

func redirectResponse(description string) map[string]any {
	return map[string]any{
		"description": description,
		"headers": map[string]any{
			"Location": map[string]any{
				"description": "Interactive login or setup route.",
				"schema":      map[string]any{"type": "string"},
			},
		},
		"content": map[string]any{
			"text/html": map[string]any{"schema": map[string]any{"type": "string"}},
		},
	}
}

func jsonResponseRef(name string) map[string]any {
	return map[string]any{
		"description": "Successful tenant-scoped response.",
		"content": map[string]any{
			"application/json": map[string]any{"schema": schemaRef(name)},
		},
	}
}

func plainTextResponse(description string) map[string]any {
	return map[string]any{
		"description": description,
		"content": map[string]any{
			"text/plain": map[string]any{"schema": map[string]any{"type": "string"}},
		},
	}
}

func jsonErrorResponse(description string) map[string]any {
	return map[string]any{
		"description": description,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"error"},
					"properties": map[string]any{
						"error": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func langParameter() map[string]any {
	return enumQueryParameter("lang", "en", "en", "ru", "sr")
}

func enumQueryParameter(name, defaultValue string, values ...string) map[string]any {
	enums := make([]any, 0, len(values))
	for _, value := range values {
		enums = append(enums, value)
	}
	return map[string]any{
		"name":        name,
		"in":          "query",
		"required":    false,
		"description": "Unsupported values are ignored and the server falls back to the documented default.",
		"schema": map[string]any{
			"type":    "string",
			"default": defaultValue,
			"enum":    enums,
		},
	}
}

func requiredEnumQueryParameter(name, description string, values ...string) map[string]any {
	parameter := enumQueryParameter(name, "", values...)
	parameter["required"] = true
	parameter["description"] = description
	schema := parameter["schema"].(map[string]any)
	delete(schema, "default")
	return parameter
}

func stringQueryParameter(name, description string) map[string]any {
	return map[string]any{
		"name":        name,
		"in":          "query",
		"required":    false,
		"description": description,
		"schema": map[string]any{
			"type":    "string",
			"format":  "date",
			"pattern": `^\d{4}-\d{2}-\d{2}$`,
		},
	}
}

func requiredStringQueryParameter(name, description string) map[string]any {
	parameter := stringQueryParameter(name, description)
	parameter["required"] = true
	schema := parameter["schema"].(map[string]any)
	delete(schema, "format")
	delete(schema, "pattern")
	return parameter
}

func requiredPathEnumParameter(name, description string, values ...string) map[string]any {
	parameter := requiredEnumQueryParameter(name, description, values...)
	parameter["in"] = "path"
	return parameter
}

func integerQueryParameter(name string, defaultValue, minimum, maximum int) map[string]any {
	return map[string]any{
		"name":     name,
		"in":       "query",
		"required": false,
		"schema": map[string]any{
			"type":    "integer",
			"default": defaultValue,
			"minimum": minimum,
			"maximum": maximum,
		},
	}
}

func reflectedSchema(value any) (map[string]any, error) {
	reflector := &jsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
	}
	schema := reflector.ReflectFromType(reflect.TypeOf(value))
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	removeSchemaMetadata(out)
	return out, nil
}

func removeSchemaMetadata(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "$schema")
		delete(typed, "$id")
		for key, child := range typed {
			if key == "properties" {
				if properties, ok := child.(map[string]any); ok {
					for propertyName, propertySchema := range properties {
						if booleanSchema, ok := propertySchema.(bool); ok {
							if booleanSchema {
								properties[propertyName] = map[string]any{}
							} else {
								properties[propertyName] = map[string]any{"not": map[string]any{}}
							}
						}
					}
				}
			}
			removeSchemaMetadata(child)
		}
	case []any:
		for _, child := range typed {
			removeSchemaMetadata(child)
		}
	}
}

func setPropertyEnum(root any, path []string, values ...string) error {
	schema, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s: root is not a schema", strings.Join(path, "."))
	}
	for _, part := range path {
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("enum target %s: properties missing before %q", strings.Join(path, "."), part)
		}
		schema, ok = properties[part].(map[string]any)
		if !ok {
			return fmt.Errorf("enum target %s: property %q missing", strings.Join(path, "."), part)
		}
	}
	schema["enum"] = stringsToAny(values)
	return nil
}

func allowPropertyNull(root any, property string) {
	schema, ok := root.(map[string]any)
	if !ok {
		return
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	propertySchema, ok := properties[property].(map[string]any)
	if !ok {
		return
	}
	typeName, ok := propertySchema["type"].(string)
	if !ok {
		return
	}
	propertySchema["type"] = []any{typeName, "null"}
}

func allowNullableArrays(value any) {
	switch schema := value.(type) {
	case map[string]any:
		if typeName, ok := schema["type"].(string); ok && typeName == "array" {
			schema["type"] = []any{"array", "null"}
		}
		for _, child := range schema {
			allowNullableArrays(child)
		}
	case []any:
		for _, child := range schema {
			allowNullableArrays(child)
		}
	}
}

func setNestedPropertyEnum(root any, parent, child string, values ...string) error {
	schema, ok := root.(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s.%s: root is not a schema", parent, child)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s.%s: root properties missing", parent, child)
	}
	parentSchema, ok := properties[parent].(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s.%s: parent missing", parent, child)
	}
	target := parentSchema
	if items, ok := parentSchema["items"].(map[string]any); ok {
		target = findObjectSchema(items)
	} else {
		target = findObjectSchema(parentSchema)
	}
	if target == nil {
		return fmt.Errorf("enum target %s.%s: object schema missing", parent, child)
	}
	childProperties, ok := target["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s.%s: child properties missing", parent, child)
	}
	childSchema, ok := childProperties[child].(map[string]any)
	if !ok {
		return fmt.Errorf("enum target %s.%s: child missing", parent, child)
	}
	childSchema["enum"] = stringsToAny(values)
	return nil
}

func findObjectSchema(schema map[string]any) map[string]any {
	if _, ok := schema["properties"]; ok {
		return schema
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		options, _ := schema[keyword].([]any)
		for _, option := range options {
			candidate, _ := option.(map[string]any)
			if candidate == nil {
				continue
			}
			if found := findObjectSchema(candidate); found != nil {
				return found
			}
		}
	}
	return nil
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// ValidateOpenAPI performs the repository's dependency-free structural checks.
// Full client generation is exercised by the frontend workspace in issue #213.
func ValidateOpenAPI(raw []byte) error {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if doc["openapi"] != OpenAPIVersion {
		return fmt.Errorf("openapi = %v, want %s", doc["openapi"], OpenAPIVersion)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		return fmt.Errorf("paths missing")
	}
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return fmt.Errorf("components missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok || len(schemas) == 0 {
		return fmt.Errorf("component schemas missing")
	}
	return validateRefs(doc, schemas)
}

func validateRefs(value any, schemas map[string]any) error {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if !strings.HasPrefix(ref, prefix) {
				return fmt.Errorf("unsupported ref %q", ref)
			}
			if _, exists := schemas[strings.TrimPrefix(ref, prefix)]; !exists {
				return fmt.Errorf("unresolved ref %q", ref)
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := validateRefs(typed[key], schemas); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateRefs(child, schemas); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompatibleResponseContract checks that a current response schema still
// accepts every shape promised by a baseline schema. Additive properties are
// allowed; removed properties, removed required guarantees, narrowed types,
// narrowed enums, and removed nullability are rejected.
func CompatibleResponseContract(baseline, current []byte) error {
	var oldDoc, newDoc map[string]any
	if err := json.Unmarshal(baseline, &oldDoc); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := json.Unmarshal(current, &newDoc); err != nil {
		return fmt.Errorf("current: %w", err)
	}
	oldSchemas, err := componentSchemas(oldDoc)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	newSchemas, err := componentSchemas(newDoc)
	if err != nil {
		return fmt.Errorf("current: %w", err)
	}
	names := make([]string, 0, len(oldSchemas))
	for name := range oldSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		newSchema, ok := newSchemas[name]
		if !ok {
			return fmt.Errorf("schema %s removed", name)
		}
		if err := compatibleSchema("components.schemas."+name, oldSchemas[name], newSchema); err != nil {
			return err
		}
	}
	if err := compatibleOperations(oldDoc, newDoc); err != nil {
		return err
	}
	return nil
}

func compatibleOperations(oldDoc, newDoc map[string]any) error {
	oldPaths, ok := oldDoc["paths"].(map[string]any)
	if !ok {
		return fmt.Errorf("baseline: paths missing")
	}
	newPaths, ok := newDoc["paths"].(map[string]any)
	if !ok {
		return fmt.Errorf("current: paths missing")
	}

	pathNames := make([]string, 0, len(oldPaths))
	for path := range oldPaths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		oldPath, ok := oldPaths[path].(map[string]any)
		if !ok {
			return fmt.Errorf("paths.%s is not a path item", path)
		}
		newPath, ok := newPaths[path].(map[string]any)
		if !ok {
			return fmt.Errorf("path %s removed", path)
		}
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			oldOperation, exists := oldPath[method]
			if !exists {
				continue
			}
			newOperation, exists := newPath[method]
			if !exists {
				return fmt.Errorf("operation %s %s removed", strings.ToUpper(method), path)
			}
			oldOperationMap, oldOK := oldOperation.(map[string]any)
			newOperationMap, newOK := newOperation.(map[string]any)
			if !oldOK || !newOK {
				return fmt.Errorf("operation %s %s changed incompatibly", strings.ToUpper(method), path)
			}
			operationPath := "paths." + path + "." + method
			if err := compatibleSuccessResponse(operationPath, oldOperationMap, newOperationMap); err != nil {
				return err
			}
			if err := compatibleParameters(operationPath, oldOperationMap, newOperationMap); err != nil {
				return err
			}
		}
	}
	return nil
}

func compatibleSuccessResponse(path string, oldOperation, newOperation map[string]any) error {
	oldSchema, err := operationResponseSchema(oldOperation, "200")
	if err != nil {
		return fmt.Errorf("%s baseline: %w", path, err)
	}
	newSchema, err := operationResponseSchema(newOperation, "200")
	if err != nil {
		return fmt.Errorf("%s current: %w", path, err)
	}
	if err := requireSuperset(path+".responses.200.refs", referenceSet(oldSchema), referenceSet(newSchema)); err != nil {
		return err
	}
	if err := compatibleDiscriminatorMapping(path+".responses.200", oldSchema, newSchema); err != nil {
		return err
	}
	return compatibleSchema(path+".responses.200.schema", oldSchema, newSchema)
}

func operationResponseSchema(operation map[string]any, status string) (map[string]any, error) {
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("responses missing")
	}
	response, ok := responses[status].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response %s missing", status)
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response %s content missing", status)
	}
	mediaType, ok := content["application/json"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response %s application/json missing", status)
	}
	schema, ok := mediaType["schema"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response %s schema missing", status)
	}
	return schema, nil
}

func compatibleParameters(path string, oldOperation, newOperation map[string]any) error {
	oldParameters := parameterMap(oldOperation["parameters"])
	newParameters := parameterMap(newOperation["parameters"])
	keys := make([]string, 0, len(oldParameters))
	for key := range oldParameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		oldParameter := oldParameters[key]
		newParameter, ok := newParameters[key]
		if !ok {
			return fmt.Errorf("%s parameter %s removed", path, key)
		}
		oldRequired, _ := oldParameter["required"].(bool)
		newRequired, _ := newParameter["required"].(bool)
		if !oldRequired && newRequired {
			return fmt.Errorf("%s parameter %s became required", path, key)
		}
		oldSchema, oldOK := oldParameter["schema"].(map[string]any)
		newSchema, newOK := newParameter["schema"].(map[string]any)
		if oldOK && !newOK {
			return fmt.Errorf("%s parameter %s schema removed", path, key)
		}
		if oldOK {
			if err := compatibleSchema(path+".parameters."+key, oldSchema, newSchema); err != nil {
				return err
			}
		}
	}
	return nil
}

func parameterMap(value any) map[string]map[string]any {
	out := map[string]map[string]any{}
	parameters, _ := value.([]any)
	for _, rawParameter := range parameters {
		parameter, ok := rawParameter.(map[string]any)
		if !ok {
			continue
		}
		name, _ := parameter["name"].(string)
		location, _ := parameter["in"].(string)
		if name != "" && location != "" {
			out[location+":"+name] = parameter
		}
	}
	return out
}

func compatibleDiscriminatorMapping(path string, oldSchema, newSchema map[string]any) error {
	oldDiscriminator, _ := oldSchema["discriminator"].(map[string]any)
	if oldDiscriminator == nil {
		return nil
	}
	newDiscriminator, _ := newSchema["discriminator"].(map[string]any)
	if newDiscriminator == nil {
		return fmt.Errorf("%s discriminator removed", path)
	}
	if oldDiscriminator["propertyName"] != newDiscriminator["propertyName"] {
		return fmt.Errorf("%s discriminator property changed", path)
	}
	oldMapping, _ := oldDiscriminator["mapping"].(map[string]any)
	newMapping, _ := newDiscriminator["mapping"].(map[string]any)
	for key, oldRef := range oldMapping {
		if newMapping[key] != oldRef {
			return fmt.Errorf("%s discriminator mapping %q removed or changed", path, key)
		}
	}
	return nil
}

func referenceSet(value any) map[string]struct{} {
	out := map[string]struct{}{}
	var collect func(any)
	collect = func(candidate any) {
		switch typed := candidate.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok {
				encoded, _ := json.Marshal(ref)
				out[string(encoded)] = struct{}{}
			}
			for _, child := range typed {
				collect(child)
			}
		case []any:
			for _, child := range typed {
				collect(child)
			}
		}
	}
	collect(value)
	return out
}

func componentSchemas(doc map[string]any) (map[string]any, error) {
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("components missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schemas missing")
	}
	return schemas, nil
}

func compatibleSchema(path string, oldValue, newValue any) error {
	oldSchema, oldOK := oldValue.(map[string]any)
	newSchema, newOK := newValue.(map[string]any)
	if !oldOK || !newOK {
		if !reflect.DeepEqual(oldValue, newValue) {
			return fmt.Errorf("%s changed incompatibly", path)
		}
		return nil
	}

	if err := requireSuperset(path+".type", valueSet(oldSchema["type"]), valueSet(newSchema["type"])); err != nil {
		return err
	}
	if err := requireSuperset(path+".enum", valueSet(oldSchema["enum"]), valueSet(newSchema["enum"])); err != nil {
		return err
	}
	if err := requireSuperset(path+".required", valueSet(oldSchema["required"]), valueSet(newSchema["required"])); err != nil {
		return err
	}

	oldProperties, _ := oldSchema["properties"].(map[string]any)
	newProperties, _ := newSchema["properties"].(map[string]any)
	propertyNames := make([]string, 0, len(oldProperties))
	for name := range oldProperties {
		propertyNames = append(propertyNames, name)
	}
	sort.Strings(propertyNames)
	for _, name := range propertyNames {
		newProperty, ok := newProperties[name]
		if !ok {
			return fmt.Errorf("%s.properties.%s removed", path, name)
		}
		if err := compatibleSchema(path+".properties."+name, oldProperties[name], newProperty); err != nil {
			return err
		}
	}

	for _, keyword := range []string{"items", "additionalProperties"} {
		if oldChild, exists := oldSchema[keyword]; exists {
			newChild, ok := newSchema[keyword]
			if !ok {
				return fmt.Errorf("%s.%s removed", path, keyword)
			}
			if err := compatibleSchema(path+"."+keyword, oldChild, newChild); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		oldOptions, oldExists := oldSchema[keyword].([]any)
		if !oldExists {
			continue
		}
		newOptions, newExists := newSchema[keyword].([]any)
		if !newExists || len(newOptions) < len(oldOptions) {
			return fmt.Errorf("%s.%s narrowed", path, keyword)
		}
		for index, oldOption := range oldOptions {
			if index >= len(newOptions) {
				return fmt.Errorf("%s.%s[%d] removed", path, keyword, index)
			}
			if err := compatibleSchema(fmt.Sprintf("%s.%s[%d]", path, keyword, index), oldOption, newOptions[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func valueSet(value any) map[string]struct{} {
	out := map[string]struct{}{}
	switch typed := value.(type) {
	case string:
		encoded, _ := json.Marshal(typed)
		out[string(encoded)] = struct{}{}
	case []any:
		for _, item := range typed {
			encoded, _ := json.Marshal(item)
			out[string(encoded)] = struct{}{}
		}
	}
	return out
}

func requireSuperset(path string, oldValues, newValues map[string]struct{}) error {
	if len(oldValues) == 0 {
		return nil
	}
	for value := range oldValues {
		if _, ok := newValues[value]; !ok {
			return fmt.Errorf("%s removed %s", path, value)
		}
	}
	return nil
}

// EqualContract ignores no fields: generated output is deterministic and any
// drift must be reviewed explicitly.
func EqualContract(expected, actual []byte) bool {
	return bytes.Equal(expected, actual)
}
