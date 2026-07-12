package parser

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	userAgent      = "v2rayNG/1.8.5"
	timeout        = 20 * time.Second
	maxBodySize    = 200 * 1024 * 1024
	previewMaxSize = 1024
)

// allowedPrefixes — поддерживаемые протоколы агрегатором.
// trojan включён: часть серверов с ним всё ещё работает.
var allowedPrefixes = []string{
	"vless://",
	"hysteria2://",
	"hy2://",
	"hysteria://",
	"trojan://",
}

var httpClient = &http.Client{
	Timeout: timeout,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // подписки могут иметь невалидные сертификаты
		},
		MaxIdleConns:        300,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
	},
	// до 10 редиректов — дефолтное поведение Go
}

// NoNodesErr — подписка загружена, но нужных протоколов нет.
type NoNodesErr struct {
	ContentType string
	Preview     string // после попытки base64-декодирования
	RawPreview  string // до декодирования
}

func (e *NoNodesErr) Error() string { return "no supported nodes found" }

// decodeBase64 обрабатывает оба типа base64-контента:
//   - однострочный (стандартные подписки)
//   - многострочный с \n каждые 76 символов (PEM-style, Gitverse и др.)
func decodeBase64(data string) ([]byte, error) {
	// Убираем ВСЕ пробельные символы, включая внутренние \n
	// Это критично для многострочного base64
	stripped := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, data)

	if len(stripped) == 0 {
		return nil, fmt.Errorf("empty input")
	}

	// Восстанавливаем паддинг
	if pad := len(stripped) % 4; pad != 0 {
		stripped += strings.Repeat("=", 4-pad)
	}

	decoded, err := base64.StdEncoding.DecodeString(stripped)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(stripped)
}

// ParseLinks извлекает поддерживаемые ссылки из текста построчно.
// Мусор в начале файла (вроде "404: Not Found\n") автоматически игнорируется.
func ParseLinks(rawText string) []string {
	var results []string
	for _, line := range strings.Split(rawText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, prefix := range allowedPrefixes {
			if strings.HasPrefix(lower, prefix) {
				results = append(results, line)
				break
			}
		}
	}
	return results
}

// detectBodyStatus проверяет, не вернул ли проксирующий сервер HTTP-статус в теле
// при формальном HTTP 200. Пример: "429: Too Many Requests\nFor more on scraping..."
func detectBodyStatus(body string) int {
	trimmed := strings.TrimSpace(body)
	// Нужно минимум "NNN: X" = 6 символов
	if len(trimmed) < 6 {
		return 0
	}
	// Первые 3 символа — цифры, 4-й — двоеточие
	if trimmed[3] != ':' {
		return 0
	}
	code := 0
	for _, ch := range trimmed[:3] {
		if ch < '0' || ch > '9' {
			return 0
		}
		code = code*10 + int(ch-'0')
	}
	if code >= 400 && code < 600 {
		return code
	}
	return 0
}

func preview(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > previewMaxSize {
		return string(runes[:previewMaxSize]) + "…"
	}
	return s
}

// FetchSubscription загружает подписку и возвращает список ссылок.
//
// Порядок обработки:
//  1. Ищем ссылки в сыром тексте (обрабатывает случай "404 в теле + реальные ноды дальше")
//  2. Если не нашли — пробуем base64-декодирование (включая многострочный)
//  3. Если ссылки найдены — возвращаем их
//  4. Если не найдены — проверяем body-level статус (429/404 с HTTP 200)
//  5. Иначе — NoNodesErr с preview для ручного анализа
func FetchSubscription(subURL string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	rawURL, err := ToRawURL(subURL)
	if err != nil {
		rawURL = subURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	rawText := string(bodyBytes)
	rawPreview := preview(rawText)

	// Шаг 1: ищем ссылки в сыром тексте.
	// ParseLinks идёт построчно, поэтому мусор вроде "404: Not Found\n"
	// перед реальными нодами автоматически отфильтровывается.
	links := ParseLinks(rawText)

	// Шаг 2: если не нашли — пробуем base64 (в т.ч. многострочный)
	decodedText := rawText
	if len(links) == 0 {
		if decoded, decErr := decodeBase64(rawText); decErr == nil {
			decodedText = string(decoded)
			links = ParseLinks(decodedText)
		}
	}

	// Шаг 3: ноды нашлись — возвращаем
	if len(links) > 0 {
		return links, nil
	}

	// Шаг 4: body-level статус-код при HTTP 200
	// (Cloudflare Workers и другие прокси возвращают "429: Too Many Requests" в теле)
	if code := detectBodyStatus(rawText); code != 0 {
		return nil, fmt.Errorf("HTTP %d", code)
	}

	// Шаг 5: подписка живая, но нужных протоколов нет — сохраняем для анализа
	return nil, &NoNodesErr{
		ContentType: contentType,
		Preview:     preview(decodedText),
		RawPreview:  rawPreview,
	}
}
