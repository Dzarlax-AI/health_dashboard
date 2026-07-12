// cmd/import imports an Apple Health export (zip or xml) into the health database.
//
// Usage:
//
//	import --file export.zip [--batch 100000] [--dry-run]
//
// The importer streams the XML so it never loads the full file into memory.
// Parsed rows are staged with bulk database operations, then promoted in one
// final transaction so parse errors do not leave partial imports behind.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"health-receiver/internal/applehealth"
	"health-receiver/internal/storage"
)

func main() {
	filePath := flag.String("file", "", "Apple Health export (.zip or export.xml) - required")
	batchSize := flag.Int("batch", 100000, "metric points or workouts per staging flush")
	pauseDur := flag.Duration("pause", 0, "deprecated; staged import ignores per-batch sleeps")
	dryRun := flag.Bool("dry-run", false, "parse only - do not write to DB")
	flag.Parse()
	_ = pauseDur
	if flag.NArg() != 0 {
		log.Fatal("positional arguments are not accepted")
	}

	if *filePath == "" {
		log.Fatal("--file is required")
	}
	if *batchSize <= 0 {
		log.Fatal("--batch must be positive")
	}
	if *pauseDur < 0 {
		log.Fatal("--pause must not be negative")
	}
	info, err := os.Stat(*filePath)
	if err != nil || !info.Mode().IsRegular() {
		log.Fatalf("--file must name a readable regular file: %v", err)
	}
	ext := strings.ToLower(filepath.Ext(*filePath))
	if ext != ".zip" && ext != ".xml" {
		log.Fatal("--file must have .zip or .xml extension")
	}
	snapshotAt := time.Now()
	var exportDateErr error
	var exportDateFound bool
	if ext == ".zip" {
		snapshotAt, exportDateFound, exportDateErr = applehealth.ExportDateFromZip(*filePath)
	} else {
		snapshotAt, exportDateFound, exportDateErr = applehealth.ExportDateFromXMLFile(*filePath)
	}
	if exportDateErr != nil {
		log.Printf("could not read Apple Health exportDate, using import start as snapshot time: %v", exportDateErr)
		snapshotAt = time.Now()
	} else if !exportDateFound {
		log.Printf("Apple Health exportDate not found, using import start as snapshot time")
		snapshotAt = time.Now()
	}

	var db *storage.DB
	var session *storage.ImportSession
	if !*dryRun {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			log.Fatal("DATABASE_URL environment variable is required")
		}
		db, err = storage.New(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("open db: %v", err)
		}
		defer db.Close()

		session, err = db.BeginAppleHealthXMLImport(storage.ImportOptions{
			SourceName: "apple-health-cli-import",
			SnapshotAt: snapshotAt,
		})
		if err != nil {
			log.Fatalf("begin import: %v", err)
		}
	}

	var (
		totalParsed         int
		totalWorkoutsParsed int
		pending             []storage.MetricPoint
		pendingWorkouts     []storage.Workout
		startTime           = time.Now()
	)

	flush := func(pts []storage.MetricPoint) {
		totalParsed += len(pts)
		if *dryRun {
			if totalParsed%100000 == 0 || totalParsed < 1000 {
				log.Printf("[dry-run] parsed %d points so far", totalParsed)
			}
			return
		}
		if err := session.AddPoints(pts); err != nil {
			session.Abort(err)
			log.Fatalf("stage points: %v", err)
		}
		if totalParsed%100000 == 0 {
			log.Printf("staged %d points so far", totalParsed)
		}
	}

	flushWorkouts := func(workouts []storage.Workout) {
		totalWorkoutsParsed += len(workouts)
		if *dryRun {
			if totalWorkoutsParsed%100 == 0 || totalWorkoutsParsed < 20 {
				log.Printf("[dry-run] parsed %d workouts so far", totalWorkoutsParsed)
			}
			return
		}
		if err := session.AddWorkouts(workouts); err != nil {
			session.Abort(err)
			log.Fatalf("stage workouts: %v", err)
		}
	}

	emit := func(pts []storage.MetricPoint) {
		pending = append(pending, pts...)
		for len(pending) >= *batchSize {
			flush(pending[:*batchSize])
			pending = pending[*batchSize:]
		}
	}

	emitWorkouts := func(workouts []storage.Workout) {
		pendingWorkouts = append(pendingWorkouts, workouts...)
		for len(pendingWorkouts) >= *batchSize {
			flushWorkouts(pendingWorkouts[:*batchSize])
			pendingWorkouts = pendingWorkouts[*batchSize:]
		}
	}

	log.Printf("starting import from %s (batch=%d dry-run=%v)", *filePath, *batchSize, *dryRun)

	opts := applehealth.EmitOptions{Points: emit, Workouts: emitWorkouts}
	var parseErr error
	switch {
	case len(*filePath) > 4 && (*filePath)[len(*filePath)-4:] == ".zip":
		parseErr = applehealth.ParseZipWithOptions(*filePath, opts, nil)
	default:
		parseErr = applehealth.ParseXMLFileWithOptions(*filePath, opts, nil)
	}
	if len(pending) > 0 {
		flush(pending)
	}
	if len(pendingWorkouts) > 0 {
		flushWorkouts(pendingWorkouts)
	}
	if parseErr != nil {
		if session != nil {
			session.Abort(parseErr)
		}
		log.Fatalf("parse error: %v", parseErr)
	}

	elapsed := time.Since(startTime).Round(time.Second)
	if *dryRun {
		log.Printf("dry-run complete: %d points and %d workouts parsed in %s", totalParsed, totalWorkoutsParsed, elapsed)
		return
	}

	counters, err := session.Commit()
	if err != nil {
		log.Fatalf("commit import: %v", err)
	}
	log.Printf("import committed: run=%d %d points parsed, %d inserted, %d updated, %d stale deleted, %d duplicate-stage skipped; %d workouts parsed, %d upserted, %d stale deleted in %s",
		counters.ImportRunID, counters.ParsedPoints, counters.InsertedPoints, counters.UpdatedPoints,
		counters.DeletedPoints, counters.SkippedPoints, counters.ParsedWorkouts, counters.UpsertedWorkouts,
		counters.DeletedWorkouts, elapsed)

	log.Println("running backfill (this may take a few minutes)")
	db.BackfillAggregates(true)
	db.BackfillScores(true)
	log.Println("backfill done - import complete")
}
