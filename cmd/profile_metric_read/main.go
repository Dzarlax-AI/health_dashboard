// Command profile_metric_read inspects one page of an isolated local dbt catalog.
// DATABASE_URL must point to a local database and a restricted profile login.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"health-receiver/internal/storage"
)

func main() {
	var request storage.CatalogMetricRequest
	source := flag.String("source-schema", "", "owned source schema")
	graph := flag.String("graph-schema", "", "owned dbt graph schema")
	key := flag.String("key", "", "optional JSON object selecting native sample key fields")
	cursor := flag.String("after", "", "optional continuation object from a preceding page")
	output := flag.String("out", "", "new private JSON file for the requested page")
	flag.StringVar(&request.MetricID, "metric", "", "catalog metric ID")
	flag.StringVar(&request.Source, "source", "", "optional producer")
	flag.StringVar(&request.DateField, "date-field", "date", "native calendar key, for example wake_date, target_date, period_day or original_date")
	flag.StringVar(&request.FromDate, "from", "", "inclusive lower bound for native date keys")
	flag.StringVar(&request.ToDate, "to", "", "inclusive upper bound for native date keys")
	flag.IntVar(&request.Limit, "limit", 20, "page size (1 to 256)")
	flag.Parse()
	if *key != "" {
		request.SampleKey = json.RawMessage(*key)
	}
	if *cursor != "" {
		request.After = &storage.CatalogMetricCursor{}
		if err := json.Unmarshal([]byte(*cursor), request.After); err != nil {
			fmt.Fprintln(os.Stderr, "invalid continuation object")
			os.Exit(2)
		}
	}
	if err := run(*source, *graph, *output, request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(source, graph, output string, request storage.CatalogMetricRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := storage.NewLocalProfileGraphReader(ctx, os.Getenv("DATABASE_URL"), source, graph)
	if err != nil {
		return errors.New("unable to open the isolated local graph")
	}
	defer db.Close()
	page, err := db.ReadCatalogMetricPage(ctx, request)
	if err != nil {
		return fmt.Errorf("catalog read failed: %w", err)
	}
	if output != "" {
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		err = json.NewEncoder(file).Encode(page)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(output)
			return errors.New("unable to write catalog page")
		}
	}
	statuses := make(map[string]int)
	for _, sample := range page.Samples {
		statuses[sample.Status]++
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Rows     int            `json:"rows"`
		HasMore  bool           `json:"has_more"`
		Statuses map[string]int `json:"statuses"`
	}{len(page.Samples), page.Next != nil, statuses})
}
