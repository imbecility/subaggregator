package parser

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	userAgent   = "v2rayNG/1.8.5"
	timeout     = 20 * time.Second
	maxBodySize = 100 * 1024 * 1024 // 100 МБ — защита от раздутых ответов
)

// allowedPrefixes — поддерживаемые протоколы (включая hy2:// alias)
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
		MaxIdleConns:        300,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	},
}

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

	rawText := string(bodyBytes)

	// Если явных URI нет — скорее всего Base64
	hasLinks := false
	for _, p := range allowedPrefixes {
		if strings.Contains(strings.ToLower(rawText), p) {
			hasLinks = true
			break
		}
	}
	if !hasLinks {
		if decoded, err := decodeBase64(rawText); err == nil {
			rawText = string(decoded)
		}
	}

	links := ParseLinks(rawText)
	if len(links) == 0 {
		return nil, fmt.Errorf("no supported nodes found")
	}
	return links, nil
}
