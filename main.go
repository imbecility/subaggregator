package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
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

	fmt.Fprintf(os.Stderr, "[INFO] Загружаем %d подписок (global=150, per-domain=5)...\n", len(urls))

	// global=150 горутин, но не более 5 одновременно к одному хосту
	results, stats := fetcher.FetchAll(urls, 150, 5)

	d := dedup.NewSet()
	var totalLinks, failedSubs int
	var output []string

	for _, r := range results {
		if r.Err != nil {
			failedSubs++
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

	// Всегда печатаем разбивку ошибок — это важная диагностика
	stats.Print()

	if err := writeErrorFiles(results); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Не удалось записать файлы ошибок: %v\n", err)
	}

	if len(output) == 0 {
		fmt.Fprintln(os.Stderr, "[ERROR] Результат пуст")
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

// writeErrorFiles создаёт директорию errors/ и пишет файлы по категориям.
func writeErrorFiles(results []fetcher.Result) error {
	dir := "errors"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Открываем файлы для каждой категории
	writers := make(map[fetcher.ErrorKind]*bufio.Writer)
	files := make(map[fetcher.ErrorKind]*os.File)

	open := func(kind fetcher.ErrorKind) (*bufio.Writer, error) {
		if w, ok := writers[kind]; ok {
			return w, nil
		}
		name := filepath.Join(dir, string(kind)+".txt")
		f, err := os.Create(name)
		if err != nil {
			return nil, err
		}
		files[kind] = f
		w := bufio.NewWriterSize(f, 256*1024)
		writers[kind] = w
		return w, nil
	}

	for _, r := range results {
		if r.Err == nil {
			continue
		}

		w, err := open(r.ErrKind)
		if err != nil {
			continue
		}

		switch r.ErrKind {
		case fetcher.KindNoNodes:
			// Для no_nodes пишем полную диагностику: URL + Content-Type + preview
			fmt.Fprintf(w, "=== %s\n", r.URL)
			if r.ContentType != "" {
				fmt.Fprintf(w, "Content-Type: %s\n", r.ContentType)
			}
			if r.RawPreview != r.BodyPreview && r.RawPreview != "" {
				fmt.Fprintf(w, "--- RAW (до base64-декодирования) ---\n%s\n", r.RawPreview)
				fmt.Fprintf(w, "--- DECODED ---\n%s\n", r.BodyPreview)
			} else if r.BodyPreview != "" {
				fmt.Fprintf(w, "--- BODY ---\n%s\n", r.BodyPreview)
			}
			fmt.Fprintln(w)

		default:
			// Для остальных достаточно URL + текст ошибки
			fmt.Fprintf(w, "%s\t%v\n", r.URL, r.Err)
		}
	}

	// Flush и закрыть все файлы
	for kind, w := range writers {
		if err := w.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] flush %s: %v\n", kind, err)
		}
	}
	for _, f := range files {
		_ = f.Close()
	}

	// Вывести что записали
	fmt.Fprintln(os.Stderr, "[INFO] Файлы ошибок записаны в errors/:")
	for kind, f := range files {
		stat, _ := f.Stat()
		if stat != nil && stat.Size() > 0 {
			fmt.Fprintf(os.Stderr, "  errors/%s.txt\n", kind)
		}
	}

	return nil
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
