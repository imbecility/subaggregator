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
	maxBodySize    = 10 * 1024 * 1024 // 10 МБ
	previewMaxSize = 512              // байт для диагностики
)

var allowedPrefixes = []string{
	"vless://",
	"hysteria2://",
	"hy2://",
	"hysteria://",
}

var httpClient = &http.Client{
	Timeout: timeout,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // намеренно: подписки могут иметь невалидные сертификаты
		},
		MaxIdleConns:        300,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	},
	// CheckRedirect не переопределяем — дефолтное поведение Go: до 10 редиректов
}

// NoNodesErr возникает когда подписка загружена успешно, но не содержит нужных протоколов.
// Содержит preview тела для ручного анализа.
type NoNodesErr struct {
	ContentType string
	Preview     string // первые 512 байт (после попытки base64-декодирования)
	RawPreview  string // первые 512 байт до декодирования
}

func (e *NoNodesErr) Error() string { return "no supported nodes found" }

func decodeBase64(data string) ([]byte, error) {
	data = strings.TrimSpace(data)
	if pad := len(data) % 4; pad != 0 {
		data += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(data)
}

// ParseLinks извлекает поддерживаемые ссылки из сырого текста.
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

func preview(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > previewMaxSize {
		return string(runes[:previewMaxSize]) + "…"
	}
	return s
}

// FetchSubscription загружает подписку, декодирует Base64 при необходимости.
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

	// Пробуем найти ссылки в сыром виде
	hasLinks := false
	for _, p := range allowedPrefixes {
		if strings.Contains(strings.ToLower(rawText), p) {
			hasLinks = true
			break
		}
	}

	rawPreview := preview(rawText)
	decodedText := rawText

	if !hasLinks {
		// Пробуем base64
		if decoded, err := decodeBase64(rawText); err == nil {
			decodedText = string(decoded)
			// Повторная проверка после декодирования
			for _, p := range allowedPrefixes {
				if strings.Contains(strings.ToLower(decodedText), p) {
					hasLinks = true
					break
				}
			}
		}
	}

	if !hasLinks {
		return nil, &NoNodesErr{
			ContentType: contentType,
			Preview:     preview(decodedText),
			RawPreview:  rawPreview,
		}
	}

	links := ParseLinks(decodedText)
	if len(links) == 0 {
		return nil, &NoNodesErr{
			ContentType: contentType,
			Preview:     preview(decodedText),
			RawPreview:  rawPreview,
		}
	}
	return links, nil
}
