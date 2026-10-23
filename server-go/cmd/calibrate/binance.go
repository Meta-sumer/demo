package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"binarytrading/internal/service/pricing"
)

// Binance public klines (GET /api/v3/klines, no API key). A kline's close is
// the last trade price inside its interval, which is the same quantity the
// platform's Binance provider records as a tick price (trade stream "p"), so
// close-to-close outcomes match the platform's last-tick-at-or-before rule.

const binanceKlinesURL = "https://api.binance.com/api/v3/klines"

// fetchSeries downloads [startMs, endMs) at stepMs (1000 for "1s", 60000 for
// "1m") and returns it on a fixed grid. Seconds or minutes the exchange
// returned no kline for are NaN (no trade); the result is cached on disk so a
// re-run of the report does not download again.
func fetchSeries(ctx context.Context, cacheDir, symbol, interval string, stepMs, startMs, endMs int64, workers int) (*pricing.Series, error) {
	n := (endMs - startMs) / stepMs
	if n <= 0 {
		return nil, errors.New("empty range")
	}
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("%s_%s_%d_%d.bin", symbol, interval, startMs, endMs))
	if s, err := readCache(cacheFile, startMs, stepMs, n); err == nil {
		return s, nil
	}

	s := &pricing.Series{StartMs: startMs, StepMs: stepMs, Close: make([]float64, n), Trades: make([]int32, n)}
	for i := range s.Close {
		s.Close[i] = math.NaN()
	}
	const page = 1000
	var pages []int64
	for t := startMs; t < endMs; t += page * stepMs {
		pages = append(pages, t)
	}
	jobs := make(chan int64)
	errs := make(chan error, len(pages))
	var wg sync.WaitGroup
	var done int
	var doneMu sync.Mutex
	client := &http.Client{Timeout: 60 * time.Second}
	for w := 0; w < max(1, workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for from := range jobs {
				to := min(endMs, from+page*stepMs) - 1
				var err error
				for attempt := 0; attempt < 5; attempt++ {
					if err = fetchPage(ctx, client, s, symbol, interval, from, to); err == nil {
						break
					}
					time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
				}
				if err != nil {
					errs <- fmt.Errorf("%s %s page %d: %w", symbol, interval, from, err)
				}
				doneMu.Lock()
				done++
				if done%100 == 0 || done == len(pages) {
					fmt.Fprintf(os.Stderr, "  %s %s: %d/%d pages\n", symbol, interval, done, len(pages))
				}
				doneMu.Unlock()
			}
		}()
	}
	for _, p := range pages {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	if err := writeCache(cacheFile, s); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not cache", cacheFile, err)
	}
	return s, nil
}

func fetchPage(ctx context.Context, client *http.Client, s *pricing.Series, symbol, interval string, from, to int64) error {
	url := binanceKlinesURL + "?symbol=" + symbol + "&interval=" + interval + "&limit=1000&startTime=" +
		strconv.FormatInt(from, 10) + "&endTime=" + strconv.FormatInt(to, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var rows [][]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return err
	}
	for _, row := range rows {
		if len(row) < 9 {
			continue
		}
		var openTime int64
		var closeStr string
		var trades int64
		if json.Unmarshal(row[0], &openTime) != nil || json.Unmarshal(row[4], &closeStr) != nil || json.Unmarshal(row[8], &trades) != nil {
			continue
		}
		price, err := strconv.ParseFloat(closeStr, 64)
		if err != nil || price <= 0 {
			continue
		}
		i := (openTime - s.StartMs) / s.StepMs
		if i < 0 || i >= int64(len(s.Close)) {
			continue
		}
		// Different pages write disjoint indices, so no lock is needed.
		s.Close[i] = price
		s.Trades[i] = int32(min(trades, math.MaxInt32))
	}
	return nil
}

func writeCache(path string, s *pricing.Series) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for i := range s.Close {
		_ = binary.Write(w, binary.LittleEndian, s.Close[i])
		_ = binary.Write(w, binary.LittleEndian, s.Trades[i])
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readCache(path string, startMs, stepMs, n int64) (*pricing.Series, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	s := &pricing.Series{StartMs: startMs, StepMs: stepMs, Close: make([]float64, n), Trades: make([]int32, n)}
	for i := int64(0); i < n; i++ {
		if err := binary.Read(r, binary.LittleEndian, &s.Close[i]); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &s.Trades[i]); err != nil {
			return nil, err
		}
	}
	return s, nil
}
