// Package applehealth parses Apple Health export XML into storage.MetricPoint values.
// It uses a streaming (token-based) XML decoder so it can handle files >2 GB
// without loading them into memory.
package applehealth

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// EmitOptions controls which Apple Health XML entities are emitted by the
// streaming parser. Callbacks are optional; nil callbacks skip that entity type.
type EmitOptions struct {
	// Records observes original identity and interval before the storage
	// projection. Seeing a record never establishes interval completeness.
	Records  func(ArchiveRecord)
	Points   func([]storage.MetricPoint)
	Workouts func([]storage.Workout)
}

// ArchiveRecord carries source evidence for consumers such as sleep episode
// candidates. A rejected projection still has its original source and times.
type ArchiveRecord struct {
	Identifier string
	Value      string
	Unit       string
	SourceName string
	Start      time.Time
	End        time.Time
	SnapshotAt time.Time
	Accepted   bool
}

// ParseZip opens the zip at path, finds apple_health_export/export.xml inside,
// and streams records into emit. onProgress (may be nil) is called periodically
// with (bytesRead, totalBytes) so callers can show a progress bar.
func ParseZip(path string, emit func([]storage.MetricPoint), onProgress func(read, total int64)) error {
	return ParseZipWithOptions(path, EmitOptions{Points: emit}, onProgress)
}

// ParseZipWithOptions opens the zip at path, finds apple_health_export/export.xml
// inside, and streams selected entities into callbacks.
func ParseZipWithOptions(path string, opts EmitOptions, onProgress func(read, total int64)) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != "apple_health_export/export.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open export.xml in zip: %w", err)
		}
		defer rc.Close()
		// Uncompressed size is known from the ZIP central directory.
		total := int64(f.UncompressedSize64)
		return ParseXMLWithOptions(newCountingReader(rc, total, onProgress), opts)
	}
	return fmt.Errorf("apple_health_export/export.xml not found in zip")
}

// ExportDateFromZip reads the snapshot timestamp in an Apple Health export zip.
func ExportDateFromZip(path string) (time.Time, bool, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name != "apple_health_export/export.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return time.Time{}, false, fmt.Errorf("open export.xml in zip: %w", err)
		}
		defer rc.Close()
		return exportDateFromXML(rc)
	}
	return time.Time{}, false, fmt.Errorf("apple_health_export/export.xml not found in zip")
}

// ParseXMLFile opens an export.xml file on disk and streams it.
func ParseXMLFile(path string, emit func([]storage.MetricPoint), onProgress func(read, total int64)) error {
	return ParseXMLFileWithOptions(path, EmitOptions{Points: emit}, onProgress)
}

// ParseXMLFileWithOptions opens an export.xml file on disk and streams selected
// entities into callbacks.
func ParseXMLFileWithOptions(path string, opts EmitOptions, onProgress func(read, total int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, _ := f.Stat()
	total := int64(0)
	if info != nil {
		total = info.Size()
	}
	return ParseXMLWithOptions(newCountingReader(f, total, onProgress), opts)
}

// ExportDateFromXMLFile reads the snapshot timestamp in an export.xml file.
func ExportDateFromXMLFile(path string) (time.Time, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false, err
	}
	defer f.Close()
	return exportDateFromXML(f)
}

type archiveSnapshotHeader struct {
	opened, done bool
	time         time.Time
	found        bool
	err          error
}

func (h *archiveSnapshotHeader) readStart(dec *xml.Decoder, start xml.StartElement) error {
	if h.done {
		return nil
	}
	if !h.opened {
		if start.Name.Local == "HealthData" {
			h.opened = true
			if value := strings.TrimSpace(attrMap(start.Attr)["exportDate"]); value != "" {
				h.setDate(value)
			}
		}
		return nil
	}
	switch start.Name.Local {
	case "ExportDate":
		h.setDate(strings.TrimSpace(attrMap(start.Attr)["value"]))
	case "Me":
		return dec.Skip()
	default:
		// The first body element closes the header even when no date was set.
		h.done = true
	}
	return nil
}

func (h *archiveSnapshotHeader) setDate(value string) {
	h.done = true
	h.time, h.found, h.err = parseExportDate(value)
}

func (h *archiveSnapshotHeader) readEnd(end xml.EndElement) {
	if h.opened && end.Name.Local == "HealthData" {
		h.done = true
	}
}

func exportDateFromXML(r io.Reader) (time.Time, bool, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var header archiveSnapshotHeader
	for !header.done {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isHarmlessXMLErr(err) {
				continue
			}
			return time.Time{}, false, fmt.Errorf("xml decode: %w", err)
		}
		switch element := tok.(type) {
		case xml.StartElement:
			if err := header.readStart(dec, element); err != nil {
				return time.Time{}, false, fmt.Errorf("xml decode: %w", err)
			}
		case xml.EndElement:
			header.readEnd(element)
		}
	}
	return header.time, header.found, header.err
}

func parseExportDate(value string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(appleTimeLayout, value)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse exportDate %q: %w", value, err)
	}
	return t, true, nil
}

// countingReader wraps an io.Reader and calls onProgress every ~1 MB read.
type countingReader struct {
	r          io.Reader
	read       int64
	total      int64
	lastReport int64
	onProgress func(read, total int64)
}

func newCountingReader(r io.Reader, total int64, onProgress func(int64, int64)) io.Reader {
	if onProgress == nil {
		return r
	}
	return &countingReader{r: r, total: total, onProgress: onProgress}
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.read += int64(n)
		if c.read-c.lastReport >= 1<<20 { // report every 1 MB
			c.onProgress(c.read, c.total)
			c.lastReport = c.read
		}
	}
	return n, err
}

// ParseXML streams XML from r, converting every <Record> element to zero or
// more MetricPoint values. emit is called with a non-empty slice every
// batchSize records.
func ParseXML(r io.Reader, emit func([]storage.MetricPoint)) error {
	return ParseXMLWithOptions(r, EmitOptions{Points: emit})
}

// ParseXMLWithOptions streams XML from r, converting supported elements to
// storage entities. Callbacks receive non-empty batches and may be nil.
func ParseXMLWithOptions(r io.Reader, opts EmitOptions) error {
	const batchSize = 2000
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var pointBatch []storage.MetricPoint
	var workoutBatch []storage.Workout
	var snapshot archiveSnapshotHeader

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Apple Health XML sometimes contains malformed entity refs; skip
			if isHarmlessXMLErr(err) {
				continue
			}
			return fmt.Errorf("xml decode: %w", err)
		}

		if opts.Records != nil && !snapshot.done {
			switch element := tok.(type) {
			case xml.StartElement:
				if err := snapshot.readStart(dec, element); err != nil {
					return fmt.Errorf("xml decode: %w", err)
				}
			case xml.EndElement:
				snapshot.readEnd(element)
			}
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "Record":
			if opts.Points == nil && opts.Records == nil {
				continue
			}
			attributes := attrMap(se.Attr)
			pts := parseRecord(attributes)
			if opts.Records != nil {
				start, _ := time.Parse(appleTimeLayout, attributes["startDate"])
				end, _ := time.Parse(appleTimeLayout, attributes["endDate"])
				opts.Records(ArchiveRecord{
					Identifier: attributes["type"], Value: attributes["value"],
					Unit: attributes["unit"], SourceName: attributes["sourceName"],
					Start: start, End: end, SnapshotAt: snapshot.time, Accepted: len(pts) != 0,
				})
			}
			if opts.Points == nil {
				continue
			}
			pointBatch = append(pointBatch, pts...)
			if len(pointBatch) >= batchSize {
				opts.Points(pointBatch)
				pointBatch = pointBatch[:0]
			}

		case "Workout":
			if opts.Workouts == nil {
				continue
			}
			var xw xmlWorkout
			if err := dec.DecodeElement(&xw, &se); err != nil {
				if isHarmlessXMLErr(err) {
					continue
				}
				return fmt.Errorf("workout decode: %w", err)
			}
			w, ok := parseWorkout(xw)
			if ok {
				workoutBatch = append(workoutBatch, w)
				if len(workoutBatch) >= batchSize {
					opts.Workouts(workoutBatch)
					workoutBatch = workoutBatch[:0]
				}
			}
		}
	}

	if len(pointBatch) > 0 && opts.Points != nil {
		opts.Points(pointBatch)
	}
	if len(workoutBatch) > 0 && opts.Workouts != nil {
		opts.Workouts(workoutBatch)
	}
	return nil
}

type xmlWorkout struct {
	ActivityType string                `xml:"workoutActivityType,attr"`
	Duration     string                `xml:"duration,attr"`
	DurationUnit string                `xml:"durationUnit,attr"`
	SourceName   string                `xml:"sourceName,attr"`
	CreationDate string                `xml:"creationDate,attr"`
	StartDate    string                `xml:"startDate,attr"`
	EndDate      string                `xml:"endDate,attr"`
	Metadata     []xmlMetadataEntry    `xml:"MetadataEntry"`
	Statistics   []xmlWorkoutStatistic `xml:"WorkoutStatistics"`
}

type xmlMetadataEntry struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

type xmlWorkoutStatistic struct {
	Type    string `xml:"type,attr"`
	Sum     string `xml:"sum,attr"`
	Average string `xml:"average,attr"`
	Maximum string `xml:"maximum,attr"`
	Unit    string `xml:"unit,attr"`
}

func parseWorkout(xw xmlWorkout) (storage.Workout, bool) {
	if xw.ActivityType == "" || xw.StartDate == "" || xw.EndDate == "" {
		return storage.Workout{}, false
	}
	start, err := time.Parse(appleTimeLayout, xw.StartDate)
	if err != nil {
		return storage.Workout{}, false
	}
	end, err := time.Parse(appleTimeLayout, xw.EndDate)
	if err != nil || end.Before(start) {
		return storage.Workout{}, false
	}

	metadata := workoutMetadataMap(xw.Metadata)
	durationSec := workoutDurationSec(xw.Duration, xw.DurationUnit, start, end)
	w := storage.Workout{
		ExternalID:  workoutExternalID(xw, metadata),
		Name:        workoutName(xw.ActivityType, metadata["HKIndoorWorkout"] == "1"),
		StartTime:   start,
		EndTime:     end,
		DurationSec: durationSec,
		IsIndoor:    metadata["HKIndoorWorkout"] == "1",
	}

	for _, stat := range xw.Statistics {
		switch stat.Type {
		case "HKQuantityTypeIdentifierActiveEnergyBurned":
			if v, ok := parseFloat(stat.Sum); ok {
				n := health.NormalizeEnergyKcal(v, stat.Unit)
				w.EnergyKcal = &n
			}
		case "HKQuantityTypeIdentifierDistanceWalkingRunning",
			"HKQuantityTypeIdentifierDistanceCycling",
			"HKQuantityTypeIdentifierDistanceSwimming",
			"HKQuantityTypeIdentifierDistanceDownhillSnowSports":
			if v, ok := parseFloat(stat.Sum); ok {
				n := health.NormalizeDistanceKm(v, stat.Unit)
				w.DistanceKm = &n
			}
		case "HKQuantityTypeIdentifierHeartRate":
			if v, ok := parseFloat(stat.Average); ok {
				w.AvgHRBPM = &v
			}
			if v, ok := parseFloat(stat.Maximum); ok {
				w.MaxHRBPM = &v
			}
		}
	}

	if v, unit, ok := parseQuantityWithUnit(metadata["HKAverageSpeed"]); ok {
		n := health.NormalizeSpeedKmh(v, unit)
		w.AvgSpeedKmh = &n
	}
	if v, unit, ok := parseQuantityWithUnit(metadata["HKMaximumSpeed"]); ok {
		n := health.NormalizeSpeedKmh(v, unit)
		w.MaxSpeedKmh = &n
	}
	if v, unit, ok := parseQuantityWithUnit(metadata["HKElevationAscended"]); ok {
		n := health.NormalizeMeters(v, unit)
		w.ElevationUpM = &n
	}
	if v, unit, ok := parseQuantityWithUnit(metadata["HKWeatherTemperature"]); ok {
		n := health.NormalizeTempC(v, unit)
		w.TemperatureC = &n
	}
	if v, _, ok := parseQuantityWithUnit(metadata["HKWeatherHumidity"]); ok {
		if v > 0 && v <= 1 {
			v *= 100
		}
		w.HumidityPct = &v
	}

	return w, true
}

func workoutMetadataMap(entries []xmlMetadataEntry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Key == "" || e.Value == "" {
			continue
		}
		if _, exists := out[e.Key]; !exists {
			out[e.Key] = e.Value
		}
	}
	return out
}

func workoutExternalID(xw xmlWorkout, metadata map[string]string) string {
	if id := strings.TrimSpace(metadata["HKMetadataKeySyncIdentifier"]); id != "" {
		return id
	}
	if id := strings.TrimSpace(metadata["HKExternalUUID"]); id != "" {
		return id
	}

	parts := []string{xw.ActivityType, xw.SourceName, xw.StartDate, xw.EndDate, xw.Duration, xw.DurationUnit}
	for _, stat := range xw.Statistics {
		parts = append(parts, stat.Type, stat.Sum, stat.Average, stat.Maximum, stat.Unit)
	}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "HKIndoorWorkout", "HKAverageSpeed", "HKMaximumSpeed", "HKElevationAscended", "HKWeatherTemperature", "HKWeatherHumidity":
			parts = append(parts, k, metadata[k])
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "applexml:" + hex.EncodeToString(sum[:12])
}

func workoutDurationSec(raw, unit string, start, end time.Time) float64 {
	if v, ok := parseFloat(raw); ok && v > 0 {
		switch strings.ToLower(strings.TrimSpace(unit)) {
		case "s", "sec", "secs", "second", "seconds":
			return v
		case "h", "hr", "hrs", "hour", "hours":
			return v * 3600
		case "m", "min", "mins", "minute", "minutes":
			return v * 60
		default:
			return v * 60
		}
	}
	return end.Sub(start).Seconds()
}

func workoutName(activityType string, indoor bool) string {
	suffix := strings.TrimPrefix(activityType, "HKWorkoutActivityType")
	switch suffix {
	case "Running":
		if indoor {
			return "Indoor Run"
		}
		return "Outdoor Run"
	case "Cycling":
		if indoor {
			return "Indoor Cycling"
		}
		return "Outdoor Cycling"
	case "Walking":
		return "Walking"
	case "FunctionalStrengthTraining":
		return "Functional Strength Training"
	case "CrossTraining":
		return "Cross Training"
	case "DownhillSkiing":
		return "Downhill Skiing"
	case "EquestrianSports":
		return "Equestrian Sports"
	case "UnderwaterDiving":
		return "Underwater Diving"
	default:
		name := splitCamelCase(suffix)
		if name == "" {
			return activityType
		}
		return name
	}
}

var quantityWithUnitRE = regexp.MustCompile(`^\s*([-+]?\d+(?:\.\d+)?)\s*(.*)\s*$`)

func parseQuantityWithUnit(s string) (float64, string, bool) {
	m := quantityWithUnitRE.FindStringSubmatch(s)
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", false
	}
	return v, strings.TrimSpace(m[2]), true
}

func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseRecord converts a single <Record> attribute map to 0–2 MetricPoints.
func parseRecord(a map[string]string) []storage.MetricPoint {
	hkType := a["type"]
	if hkType == "" {
		return nil
	}
	// Only process Quantity and Category types; skip Correlation, Audiogram, etc.
	if !strings.HasPrefix(hkType, "HKQuantityTypeIdentifier") &&
		!strings.HasPrefix(hkType, "HKCategoryTypeIdentifier") {
		return nil
	}

	source := a["sourceName"]
	startDate := a["startDate"]
	endDate := a["endDate"]

	// ── Sleep analysis ────────────────────────────────────────────────────────
	metricName, categoryUnit, categoryMode := dbtmetrics.ResolveHealthKitCategory(hkType, a["value"])
	switch categoryMode {
	case "sleep_stages":
		if metricName == "" {
			return nil // InBed or unknown — skip
		}
		dur := durationHours(startDate, endDate)
		if dur <= 0 {
			return nil
		}
		pts := []storage.MetricPoint{
			{MetricName: metricName, Units: categoryUnit, Date: startDate, Qty: dur, Source: source},
		}
		// Also emit sleep_total for every "asleep" stage (aggregated by storage layer)
		if metricName != "sleep_awake" {
			pts = append(pts, storage.MetricPoint{
				MetricName: "sleep_total", Units: categoryUnit, Date: startDate, Qty: dur, Source: source,
			})
		}
		return pts
	case "stand_hour":
		qty := 0.0
		if a["value"] == "HKCategoryValueAppleStandHourStood" {
			qty = 1
		}
		return []storage.MetricPoint{
			{MetricName: metricName, Units: categoryUnit, Date: startDate, Qty: qty, Source: source},
		}
	case "duration_minutes":
		dur := durationHours(startDate, endDate) * 60 // convert to minutes
		if dur <= 0 {
			return nil
		}
		return []storage.MetricPoint{
			{MetricName: metricName, Units: categoryUnit, Date: startDate, Qty: dur, Source: source},
		}
	case "count":
		if metricName == "" {
			return nil
		}
		return []storage.MetricPoint{
			{MetricName: metricName, Units: categoryUnit, Date: startDate, Qty: 1, Source: source},
		}
	case "skip":
		return nil
	}
	value, err := strconv.ParseFloat(a["value"], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	unit := a["unit"]
	if definition, ok := dbtmetrics.ResolveHealthKitQuantity(hkType); ok {
		if unit == "" {
			if definition.XMLRequiresUnit {
				return nil
			}
			unit = definition.XMLDefaultUnit
		}
		if definition.XMLFraction && value > 0 && value <= 1 {
			value *= 100
		}
		point := storage.CanonicalMetricPoint(storage.MetricPoint{
			MetricName: definition.Key, Units: unit, Date: startDate, Qty: value, Source: source,
		})
		if definition.XMLRequiresUnit && point.Units != definition.Unit {
			return nil
		}
		if definition.XMLLegacyKey != "" && health.IsImpossible(definition.XMLLegacyKey, value) {
			return nil
		}
		return []storage.MetricPoint{point}
	}

	// Keep numeric records with no declared route only when their fallback key is distinct.
	metricName = toSnakeCase(hkTypeSuffix(hkType))
	if !dbtmetrics.HealthKitFallbackKeyAllowed(metricName) {
		return nil
	}
	return []storage.MetricPoint{
		{MetricName: metricName, Units: unit, Date: startDate, Qty: value, Source: source},
	}
}

// hkTypeSuffix returns the part after the last "Identifier" in a HK type string.
// e.g. "HKQuantityTypeIdentifierStepCount" → "StepCount"
func hkTypeSuffix(t string) string {
	if i := strings.LastIndex(t, "Identifier"); i >= 0 {
		return t[i+len("Identifier"):]
	}
	return t
}

// attrMap converts []xml.Attr to map[name]value.
func attrMap(attrs []xml.Attr) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Name.Local] = a.Value
	}
	return m
}

const appleTimeLayout = "2006-01-02 15:04:05 -0700"

// durationHours returns (end - start) in hours, or 0 on parse error.
func durationHours(start, end string) float64 {
	s, err1 := time.Parse(appleTimeLayout, start)
	e, err2 := time.Parse(appleTimeLayout, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	h := e.Sub(s).Hours()
	if h < 0 {
		return 0
	}
	return h
}

// isHarmlessXMLErr returns true for XML errors we can safely skip over
// (e.g. unknown HTML entities embedded in device strings).
func isHarmlessXMLErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "invalid character entity") ||
		strings.Contains(s, "undefined entity")
}

// toSnakeCase converts a CamelCase HK suffix to snake_case.
// e.g. "SomeNewMetric" → "some_new_metric"
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32) // toLower
		} else {
			b.WriteRune(r)
		}
	}
	result := b.String()
	// Trim leading underscore if suffix started with uppercase (always true)
	return strings.TrimPrefix(result, "_")
}

func splitCamelCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
