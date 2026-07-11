package fetcher

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imbecility/subaggregator/parser"
)

// Result — результат загрузки одной подписки.
type Result struct {
	URL   string
	Links []string
	Err   error
}

// FetchAll параллельно загружает все подписки.
// concurrency — максимальное число одновременных запросов.
func FetchAll(urls []string, concurrency int) []Result {
	sem := make(chan struct{}, concurrency)
	results := make([]Result, len(urls))
	var wg sync.WaitGroup

	var done atomic.Int64
	total := int64(len(urls))

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, subURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			links, err := fetchWithRetry(subURL, 2)
			results[idx] = Result{URL: subURL, Links: links, Err: err}

			n := done.Add(1)
			if n%500 == 0 || n == total {
				fmt.Fprintf(os.Stderr, "[INFO] Прогресс: %d/%d подписок\n", n, total)
			}
		}(i, u)
	}

	wg.Wait()
	return results
}

// fetchWithRetry выполняет загрузку с повтором при ошибке.
func fetchWithRetry(subURL string, maxRetries int) ([]string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		links, err := parser.FetchSubscription(subURL)
		if err == nil {
			return links, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
