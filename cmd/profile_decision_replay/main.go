// Command profile_decision_replay verifies a product calculation on a
// restricted local copy. Private receipts can be replayed after later syncs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"health-receiver/internal/storage"
)

func main() {
	metric := flag.String("metric", "energy", "energy, readiness-history, readiness-morning, or today (-journal)")
	journal := flag.Bool("journal", false, "compare stored decision revisions for the supplied date")
	source := flag.String("source-schema", "", "owned profile source schema")
	graph := flag.String("graph-schema", "", "installed local graph schema")
	inputs := flag.String("energy-schema", "", "owned Energy inputs used by the installed graph")
	day := flag.String("date", "", "target date for a new calculation")
	zone := flag.String("timezone", "UTC", "time zone for the dated product calculation")
	output := flag.String("save", "", "new private file to preserve a verified capture")
	replay := flag.String("replay", "", "replay a saved private capture")
	flag.Parse()
	if err := run(*journal, *metric, *source, *graph, *inputs, *day, *zone, *output, *replay); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Verified local decision against the installed dbt model.")
}
func run(journal bool, metric, source, graph, inputs, day, zone, output, replay string) error {
	if (day == "") == (replay == "") || (replay != "" && output != "") {
		return errors.New("supply a date for a new capture or a saved packet to replay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := storage.NewLocalProfileGraphReader(ctx, os.Getenv("DATABASE_URL"), source, graph)
	if err != nil {
		return errors.New("unable to open the isolated profile graph")
	}
	defer db.Close()
	if journal {
		if replay != "" || output != "" || day == "" {
			return errors.New("journal comparison requires a date")
		}
		result, err := db.CompareLocalDecisionJournal(ctx, metric, day)
		if err != nil {
			return err
		}
		if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
		if result.Recorded == 0 {
			return errors.New("no captured decisions for the requested date")
		}
		if result.Recorded != result.Matched {
			return errors.New("journal comparison incomplete")
		}
		return nil
	}
	var energy storage.LocalEnergyDecision
	var readiness storage.LocalReadinessDecision
	var packet any
	switch metric {
	case "energy":
		packet = &energy
	case "readiness-history", "readiness-morning":
		packet = &readiness
	default:
		return errors.New("unknown decision metric")
	}
	if replay != "" {
		file, err := os.Open(replay)
		if err != nil {
			return err
		}
		defer file.Close()
		if err = json.NewDecoder(file).Decode(packet); err != nil {
			return errors.New("invalid decision capture")
		}
	} else {
		if metric == "energy" {
			energy, err = db.CaptureLocalEnergyDecision(ctx, zone, day)
		} else {
			readiness, err = db.CaptureLocalReadinessDecision(ctx, day, strings.TrimPrefix(metric, "readiness-"))
		}
		if err != nil {
			return err
		}
	}
	if metric == "energy" {
		err = db.VerifyLocalEnergyDecision(ctx, inputs, energy)
	} else {
		if readiness.Reader != strings.TrimPrefix(metric, "readiness-") {
			return errors.New("saved packet uses a different Readiness reader")
		}
		err = db.VerifyLocalReadinessDecision(ctx, readiness)
	}
	if err != nil {
		return err
	}
	if output == "" {
		return nil
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writeErr := json.NewEncoder(f).Encode(packet)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(output)
		return errors.New("could not save decision capture")
	}
	return nil
}
