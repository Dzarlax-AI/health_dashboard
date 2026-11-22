package ui

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/ctxdb"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
	"health-receiver/internal/testdb"
)

func TestSleepSourceSettingsPage(t *testing.T) {
	for _, lang := range []string{"en", "ru", "sr"} {
		t.Run(lang, func(t *testing.T) {
			for _, key := range []string{"title", "label", "automatic", "hint", "unavailable", "empty", "saved", "save_error", "load_error"} {
				if translations[lang]["sleep_source_"+key] == "" {
					t.Fatalf("missing %q translation", key)
				}
			}
			h := &Handler{mgr: &tenants.Manager{}}
			w := httptest.NewRecorder()
			h.pageSettings(w, httptest.NewRequest("GET", "/settings?lang="+lang, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("settings render returned %d", w.Code)
			}
			body := html.UnescapeString(w.Body.String())
			for _, text := range []string{T(lang, "sleep_source_title"), T(lang, "sleep_source_label"), T(lang, "sleep_source_automatic"), T(lang, "sleep_source_hint"), `for="cfg-sleep-source"`} {
				if !strings.Contains(body, text) {
					t.Errorf("missing %q on settings page", text)
				}
			}
		})
	}
}

func TestSleepSourceSettingsTenantSelection(t *testing.T) {
	first, firstSchema, cleanup := testTenantDB(t)
	defer cleanup()
	ctx := context.Background()
	secondSchema := testdb.SchemaName("sleep_source_settings")
	if err := testdb.CreateSchema(ctx, sharedTenantPool, secondSchema); err != nil {
		t.Fatal(err)
	}
	defer testdb.DropSchema(ctx, sharedTenantPool, secondSchema)
	secondPool, err := testdb.NewPool(ctx, testdb.DSN(t), secondSchema)
	if err != nil {
		t.Fatal(err)
	}
	second := storage.NewFromPool(secondPool)
	defer second.Close()
	if err := second.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	for _, db := range []*storage.DB{first, second} {
		if err := db.EnsureIndexesContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{mgr: &tenants.Manager{}}
	request := func(db *storage.DB, schema, method, path string, fields map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var body string
		if fields != nil {
			data, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			body = string(data)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		ctx := ctxdb.WithIsAdmin(ctxdb.WithDB(r.Context(), db, schema), false)
		w := httptest.NewRecorder()
		h.userSettings(w, r.WithContext(ctx))
		return w
	}
	get := func(db *storage.DB, schema string) clientapi.NotificationSettingsResponse {
		t.Helper()
		w := request(db, schema, "GET", "/api/settings", nil)
		var current clientapi.NotificationSettingsResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &current) != nil {
			_, _, sourceErr := db.SleepSourcePreference(context.Background())
			t.Fatalf("settings read failed (%d): %v", w.Code, sourceErr)
		}
		return current
	}
	save := func(source string, want int) {
		t.Helper()
		w := request(first, firstSchema, "POST", "/api/settings", map[string]string{storage.PreferredSleepSourceSetting: source})
		if w.Code != want {
			t.Fatalf("settings save status %d, want %d", w.Code, want)
		}
	}
	add := func(db *storage.DB, source string) {
		t.Helper()
		id, err := db.InsertRaw(storage.Record{Payload: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		if err = db.InsertPoints(id, []storage.MetricPoint{{MetricName: "sleep_total", Units: "hr", Date: "2026-10-04 07:00:00 +0000", Qty: 7.4, Source: source}}); err != nil {
			t.Fatal(err)
		}
	}
	initial := get(first, firstSchema)
	if initial.PreferredSleepSource != "" || initial.SleepSources == nil || len(initial.SleepSources) != 0 {
		t.Fatal("a new profile must use Automatic with an empty list")
	}
	const watch = `Watch <left> & "right"`
	add(first, watch)
	add(second, "Other watch")
	if got := get(first, firstSchema); !slices.Equal(got.SleepSources, []string{watch}) {
		t.Fatal("unexpected profile source list", got.SleepSources)
	}
	save(watch, http.StatusOK)
	if got := get(first, firstSchema); got.PreferredSleepSource != watch {
		t.Fatal("exact preference did not survive save")
	}
	if got := get(second, secondSchema); got.PreferredSleepSource != "" || !slices.Equal(got.SleepSources, []string{"Other watch"}) {
		t.Fatal("preferences crossed profiles")
	}
	w := request(first, firstSchema, "POST", "/api/settings", map[string]string{"report_lang": "sr", storage.PreferredSleepSourceSetting: "Other watch"})
	if w.Code != http.StatusBadRequest || first.GetSettingExists("report_lang") || get(first, firstSchema).PreferredSleepSource != watch {
		t.Fatal("invalid source must reject the entire settings change")
	}
	w = request(first, firstSchema, "POST", "/api/settings?schema="+url.QueryEscape(secondSchema), map[string]string{storage.PreferredSleepSourceSetting: "Other watch"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin selected another profile (%d)", w.Code)
	}
	if _, err := sharedTenantPool.Exec(context.Background(), `DELETE FROM metric_points WHERE source=$1`, watch); err != nil {
		t.Fatal(err)
	}
	got := get(first, firstSchema)
	if got.PreferredSleepSource != watch || len(got.SleepSources) != 0 {
		t.Fatal("disappeared source must stay identifiable")
	}
	save("", http.StatusOK)
	if got := get(first, firstSchema); got.PreferredSleepSource != "" {
		t.Fatal("Automatic did not clear old preference")
	}
}
