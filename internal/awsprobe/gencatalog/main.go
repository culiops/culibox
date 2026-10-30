// Command gencatalog snapshots AWS's machine-readable service reference into
// internal/awsprobe/actions.json.gz, the catalog the credential-minting screen
// expands IAM action patterns against. Run it from the repository root:
//
//	go run ./internal/awsprobe/gencatalog
//
// then set CatalogDate in catalog.go to the date it printed. The output is
// deterministic (sorted keys, gzip header without mtime or name), so a rerun
// against an unchanged reference produces an identical file.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

const indexURL = "https://servicereference.us-east-1.amazonaws.com/"

type indexEntry struct {
	Service string `json:"service"`
	URL     string `json:"url"`
}

type serviceDoc struct {
	Name    string `json:"Name"`
	Actions []struct {
		Name string `json:"Name"`
	} `json:"Actions"`
}

// catalog is the on-disk shape: IAM service prefix -> sorted action names.
type catalog struct {
	Date     string              `json:"date"`
	Services map[string][]string `json:"services"`
}

var client = &http.Client{Timeout: 60 * time.Second}

func get(url string, v any) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		var resp *http.Response
		resp, err = client.Get(url)
		if err != nil {
			continue
		}
		var body []byte
		body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
		}
		if err != nil {
			continue
		}
		if err = json.Unmarshal(body, v); err == nil {
			return nil
		}
	}
	return err
}

func main() {
	out := flag.String("o", "internal/awsprobe/actions.json.gz", "output file")
	flag.Parse()

	var index []indexEntry
	if err := get(indexURL, &index); err != nil {
		fatal(err)
	}
	if len(index) < 100 {
		fatal(fmt.Errorf("service index has only %d entries; refusing to write a partial catalog", len(index)))
	}

	cat := catalog{Date: time.Now().UTC().Format("2006-01-02"), Services: map[string][]string{}}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 8)
		firstErr error
	)
	for _, e := range index {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var d serviceDoc
			err := get(e.URL, &d)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			prefix := strings.ToLower(d.Name)
			if prefix == "" {
				prefix = strings.ToLower(e.Service)
			}
			for _, a := range d.Actions {
				cat.Services[prefix] = append(cat.Services[prefix], a.Name)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		fatal(firstErr)
	}
	total := 0
	for p, as := range cat.Services {
		slices.Sort(as)
		cat.Services[p] = slices.Compact(as)
		total += len(cat.Services[p])
	}

	raw, err := json.Marshal(cat) // map keys are sorted by encoding/json
	if err != nil {
		fatal(err)
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // zero Header: no mtime, no name
	zw.Write(raw)
	zw.Close()
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: date=%s services=%d actions=%d gz=%d bytes\n", *out, cat.Date, len(cat.Services), total, buf.Len())
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gencatalog:", err)
	os.Exit(1)
}
