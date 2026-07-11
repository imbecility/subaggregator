package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// normalizeScheme приводит схему к каноническому виду.
// hy2:// и hysteria2:// — один протокол.
func normalizeScheme(raw string) string {
	switch strings.ToLower(raw) {
	case "hy2":
		return "hysteria2"
	default:
		return strings.ToLower(raw)
	}
}

// NormalizeLink нормализует схему и удаляет мусорный фрагмент (#name).
// Чекер потом сам проставит теги по результатам проверки.
func NormalizeLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Scheme = normalizeScheme(u.Scheme)
	u.Fragment = ""
	return u.String()
}

// fingerprint вычисляет стабильный 128-битный ключ для ноды.
//
// Дублем считаются ссылки, у которых совпадают:
//   - протокол (нормализованный)
//   - идентификатор пользователя (UUID / password)
//   - host:port
//   - транспортный тип (tcp/ws/grpc/h2)
//   - путь транспорта (path для ws/h2, serviceName для grpc)
//   - уровень безопасности (none/tls/reality)
//   - публичный ключ Reality (pbk) — идентифицирует конкретный сервер
//   - shortId Reality (sid)
//   - flow (xtls-rprx-vision и т.п.)
func fingerprint(rawLink string) (string, error) {
	u, err := url.Parse(rawLink)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}

	q := u.Query()

	scheme := normalizeScheme(u.Scheme)

	userID := ""
	if u.User != nil {
		userID = u.User.Username()
		if pwd, ok := u.User.Password(); ok && pwd != "" {
			userID += ":" + pwd
		}
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "443"
	}

	netType := strings.ToLower(q.Get("type"))
	if netType == "" {
		netType = "tcp"
	}

	// Ключ собирается детерминированно — порядок важен
	parts := []string{
		scheme,
		userID,
		host + ":" + port,
		netType,
		q.Get("path"),        // ws / h2 path
		q.Get("serviceName"), // grpc
		strings.ToLower(q.Get("security")),
		q.Get("pbk"), // Reality: публичный ключ сервера
		q.Get("sid"), // Reality: shortId
		strings.ToLower(q.Get("flow")),
	}

	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16]), nil // 128 бит — коллизии исключены
}

// Set — структура для дедупликации (не потокобезопасна, используется после сбора).
type Set struct {
	seen map[string]struct{}
}

func NewSet() *Set {
	return &Set{seen: make(map[string]struct{})}
}

// Add возвращает true, если ссылка уникальна и была добавлена.
func (s *Set) Add(link string) bool {
	fp, err := fingerprint(link)
	if err != nil {
		return false
	}
	if _, exists := s.seen[fp]; exists {
		return false
	}
	s.seen[fp] = struct{}{}
	return true
}

func (s *Set) Len() int { return len(s.seen) }
