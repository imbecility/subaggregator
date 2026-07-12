package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/imbecility/subaggregator/dedup"
	"github.com/imbecility/subaggregator/fetcher"
	"github.com/imbecility/subaggregator/publisher"
)

func main() {
	var (
		outputPath    string
		gitverseRepo  string
		gitverseToken string
		gitverseTag   string
	)

	flag.StringVar(&outputPath, "output", "sub.txt.gz", "Путь к выходному файлу (gzip-сжатый)")
	flag.StringVar(&gitverseRepo, "gitverse-repo", "", "Репозиторий GitVerse в формате owner/repo")
	flag.StringVar(&gitverseToken, "gitverse-token", "", "API-токен GitVerse")
	flag.StringVar(&gitverseTag, "gitverse-tag", "sub", "Тег релиза на GitVerse")
	flag.Parse()

	subsFile := "subscriptions.txt"
	if flag.NArg() > 0 {
		subsFile = flag.Arg(0)
	}

	urls := loadURLs(subsFile)
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

	stats.Print()

	if err := writeErrorFiles(results); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Не удалось записать файлы ошибок: %v\n", err)
	}

	if len(output) == 0 {
		fmt.Fprintln(os.Stderr, "[ERROR] Результат пуст")
		os.Exit(1)
	}

	if err := writeGzip(outputPath, output); err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Запись файла: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[INFO] Записан %s (%d уникальных конфигов)\n", outputPath, uniqueCount)

	// GitVerse публикация — нефатальна, только предупреждаем при ошибке
	if gitverseRepo != "" && gitverseToken != "" {
		fmt.Fprintln(os.Stderr, "[INFO] Публикуем на GitVerse...")
		if err := publisher.PublishToGitverse(gitverseToken, gitverseRepo, gitverseTag, outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] GitVerse: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "[INFO] GitVerse: опубликовано успешно")
		}
	}
}

// writeGzip сжимает строки в gzip-файл с максимальным уровнем сжатия.
func writeGzip(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}

	bw := bufio.NewWriterSize(gz, 4*1024*1024)
	for _, line := range lines {
		if _, err := fmt.Fprintln(bw, line); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return gz.Close() // финализирует gzip-поток (чексумма и ISIZE)
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

func loadURLs(filename string) []string {
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
