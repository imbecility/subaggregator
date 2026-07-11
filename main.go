package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/imbecility/subaggregator/dedup"
	"github.com/imbecility/subaggregator/fetcher"
)

func main() {
	urls := loadURLs()
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "[ERROR] Список подписок пуст")
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[INFO] Загружаем %d подписок (concurrency=150)...\n", len(urls))
	results := fetcher.FetchAll(urls, 150)

	d := dedup.NewSet()
	var totalLinks, failedSubs int
	var output []string

	for _, r := range results {
		if r.Err != nil {
			failedSubs++
			// Подробный лог ошибок — только в отладочном режиме
			if os.Getenv("AGGREGATOR_VERBOSE") != "" {
				fmt.Fprintf(os.Stderr, "[WARN] %s: %v\n", r.URL, r.Err)
			}
			continue
		}
		for _, link := range r.Links {
			totalLinks++
			normalized := dedup.NormalizeLink(link)
			if d.Add(normalized) {
				output = append(output, normalized)
			}
		}
	}

	uniqueCount := d.Len()
	dupeCount := totalLinks - uniqueCount

	fmt.Fprintf(os.Stderr,
		"[INFO] Подписок: %d загружено, %d с ошибкой\n",
		len(urls)-failedSubs, failedSubs,
	)
	fmt.Fprintf(os.Stderr,
		"[INFO] Конфигов: %d всего | %d уникальных | %d дублей (%.1f%%)\n",
		totalLinks, uniqueCount, dupeCount,
		float64(dupeCount)/float64(max(totalLinks, 1))*100,
	)

	if len(output) == 0 {
		fmt.Fprintln(os.Stderr, "[ERROR] Результат пуст — ничего не записано")
		os.Exit(1)
	}

	// Результат — в stdout (GitHub Actions перенаправит в файл)
	w := bufio.NewWriterSize(os.Stdout, 4*1024*1024)
	for _, link := range output {
		fmt.Fprintln(w, link)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Flush: %v\n", err)
		os.Exit(1)
	}
}

func loadURLs() []string {
	filename := "subscriptions.txt"
	if len(os.Args) > 1 {
		filename = os.Args[1]
	}

	f, err := os.Open(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Не удалось открыть %s: %v\n", filename, err)
		return nil
	}
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls
}
