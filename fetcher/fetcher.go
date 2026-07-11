package fetcher

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imbecility/subaggregator/parser"
)

// ErrorKind — категория ошибки для статистики.
type ErrorKind string

const (
	KindTimeout   ErrorKind = "timeout"
	KindDNS       ErrorKind = "dns"
	KindRefused   ErrorKind = "refused"
	KindTLS       ErrorKind = "tls"
	KindHTTP4xx   ErrorKind = "http_4xx"
	KindHTTP5xx   ErrorKind = "http_5xx"
	KindNoNodes   ErrorKind = "no_nodes"
	KindRateLimit ErrorKind = "rate_limit_429"
	KindOther     ErrorKind = "other"
)

// Result — результат загрузки одной подписки.
type Result struct {
	URL     string
	Links   []string
	Err     error
	ErrKind ErrorKind
}

// Stats — агрегированная статистика по ошибкам.
type Stats struct {
	mu      sync.Mutex
	counts  map[ErrorKind]int
	samples map[ErrorKind]string // по одному примеру на каждый вид
}

func NewStats() *Stats {
	return &Stats{
		counts:  make(map[ErrorKind]int),
		samples: make(map[ErrorKind]string),
	}
}

func (s *Stats) Record(kind ErrorKind, url string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[kind]++
	if _, exists := s.samples[kind]; !exists {
		s.samples[kind] = fmt.Sprintf("%s → %v", url, err)
	}
}

func (s *Stats) Print() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.counts) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr, "\n[INFO] Разбивка ошибок по типу:")
	order := []ErrorKind{
		KindRateLimit, KindTimeout, KindHTTP4xx, KindHTTP5xx,
		KindNoNodes, KindDNS, KindRefused, KindTLS, KindOther,
	}
	for _, k := range order {
		n := s.counts[k]
		if n == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-15s %4d    пример: %s\n", k, n, truncate(s.samples[k], 120))
	}
	fmt.Fprintln(os.Stderr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// classifyError определяет тип ошибки по её содержимому.
func classifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())

	switch {
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "too many"):
		return KindRateLimit
	case strings.Contains(msg, "http 4"):
		return KindHTTP4xx
	case strings.Contains(msg, "http 5"):
		return KindHTTP5xx
	case strings.Contains(msg, "no supported nodes"):
		return KindNoNodes
	case strings.Contains(msg, "certificate") || strings.Contains(msg, "tls") || strings.Contains(msg, "x509"):
		return KindTLS
	case strings.Contains(msg, "connection refused"):
		return KindRefused
	}

	// Проверяем через errors/net для точной классификации
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return KindTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return KindTimeout
		}
		return KindDNS
	}

	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") || strings.Contains(msg, "context canceled") {
		return KindTimeout
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup") {
		return KindDNS
	}

	return KindOther
}

// domainLimiter ограничивает число одновременных запросов к одному хосту.
type domainLimiter struct {
	mu   sync.Mutex
	sems map[string]chan struct{}
	n    int
}

func newDomainLimiter(perDomain int) *domainLimiter {
	return &domainLimiter{
		sems: make(map[string]chan struct{}),
		n:    perDomain,
	}
}

func (l *domainLimiter) acquire(rawURL string) {
	host := extractHost(rawURL)
	l.mu.Lock()
	sem, ok := l.sems[host]
	if !ok {
		sem = make(chan struct{}, l.n)
		l.sems[host] = sem
	}
	l.mu.Unlock()
	sem <- struct{}{}
}

func (l *domainLimiter) release(rawURL string) {
	host := extractHost(rawURL)
	l.mu.Lock()
	sem := l.sems[host]
	l.mu.Unlock()
	<-sem
}

func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	// Группируем поддомены под корневой домен, чтобы лимит был на всё семейство.
	// user1.serv00.net и user2.serv00.net — один хост для rate-limiting.
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return host
}

// FetchAll параллельно загружает все подписки с per-domain ограничением.
func FetchAll(urls []string, globalConcurrency, perDomainConcurrency int) ([]Result, *Stats) {
	globalSem := make(chan struct{}, globalConcurrency)
	dl := newDomainLimiter(perDomainConcurrency)
	stats := NewStats()

	results := make([]Result, len(urls))
	var wg sync.WaitGroup

	var done atomic.Int64
	total := int64(len(urls))

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, subURL string) {
			defer wg.Done()

			globalSem <- struct{}{}
			defer func() { <-globalSem }()

			dl.acquire(subURL)
			defer dl.release(subURL)

			links, err := fetchWithRetry(subURL, 2)
			kind := classifyError(err)

			if err != nil {
				stats.Record(kind, subURL, err)
			}

			results[idx] = Result{URL: subURL, Links: links, Err: err, ErrKind: kind}

			n := done.Add(1)
			if n%250 == 0 || n == total {
				fmt.Fprintf(os.Stderr, "[INFO] Прогресс: %d/%d\n", n, total)
			}
		}(i, u)
	}

	wg.Wait()
	return results, stats
}

// fetchWithRetry выполняет загрузку с повтором при временных ошибках.
func fetchWithRetry(subURL string, maxRetries int) ([]string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Экспоненциальный бэкофф с джиттером
			sleep := time.Duration(attempt*attempt)*time.Second + time.Duration(attempt*300)*time.Millisecond
			time.Sleep(sleep)
		}
		links, err := parser.FetchSubscription(subURL)
		if err == nil {
			return links, nil
		}
		lastErr = err

		// Не ретраим клиентские ошибки — они не исправятся сами
		kind := classifyError(err)
		if kind == KindHTTP4xx || kind == KindDNS || kind == KindNoNodes {
			break
		}
	}
	return nil, lastErr
}
