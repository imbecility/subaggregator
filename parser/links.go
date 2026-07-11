package parser

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ToRawURL конвертирует любой вариант GitHub/GitVerse-ссылки в прямую raw-ссылку.
func ToRawURL(inputURL string) (string, error) {
	u, err := url.Parse(inputURL)
	if err != nil {
		return "", fmt.Errorf("невалидный URL: %w", err)
	}

	segments := splitPath(u.Path)

	switch u.Host {
	case "raw.githubusercontent.com":
		return normalizeRawURL(segments)
	case "github.com":
		return convertGitHubURL(segments)
	case "gitverse.ru":
		return convertGitVerseURL(segments)
	default:
		return inputURL, nil
	}
}

// splitPath разбивает путь URL на непустые сегменты.
func splitPath(path string) []string {
	raw := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildRawURL собирает финальную raw.githubusercontent.com ссылку.
func buildRawURL(owner, repo, branchOrSHA string, fileParts []string) string {
	return fmt.Sprintf(
		"https://raw.githubusercontent.com/%s/%s/%s/%s",
		owner, repo, branchOrSHA, strings.Join(fileParts, "/"),
	)
}

// normalizeRawURL нормализует уже raw-ссылки (убирает refs/heads/ если есть).
//
// Форматы:
//
//	raw.githubusercontent.com/{owner}/{repo}/refs/heads/{branch}/{file}
//	raw.githubusercontent.com/{owner}/{repo}/{branch}/{file}
func normalizeRawURL(segments []string) (string, error) {
	// минимум: [owner, repo, branch, file]
	if len(segments) < 4 {
		return "", errors.New("raw.githubusercontent.com URL слишком короткий")
	}

	owner, repo := segments[0], segments[1]
	rest := segments[2:]

	var branch string
	var fileParts []string

	if len(rest) >= 3 && rest[0] == "refs" && rest[1] == "heads" {
		// refs/heads/{branch}/{file...}
		branch = rest[2]
		fileParts = rest[3:]
	} else {
		// {branch}/{file...}
		branch = rest[0]
		fileParts = rest[1:]
	}

	if len(fileParts) == 0 {
		return "", errors.New("не найден путь к файлу в raw URL")
	}

	return buildRawURL(owner, repo, branch, fileParts), nil
}

// convertGitHubURL обрабатывает ссылки с github.com.
//
// Поддерживаемые форматы:
//
//	/blob/{branch|sha}/{file...}
//	/blame/{branch}/{file...}
//	/raw/refs/heads/{branch}/{file...}
//	/raw/{branch}/{file...}
//	/{file...}  — ветка по умолчанию "main"
func convertGitHubURL(segments []string) (string, error) {
	if len(segments) < 3 {
		return "", errors.New("URL должен содержать owner, repo и путь к файлу")
	}

	owner, repo := segments[0], segments[1]
	verb := segments[2]

	switch verb {
	case "blob", "blame":
		// /{verb}/{branch|sha}/{file...}
		if len(segments) < 5 {
			return "", fmt.Errorf("%q URL требует ветку и путь к файлу", verb)
		}
		branch := segments[3]
		fileParts := segments[4:]
		return buildRawURL(owner, repo, branch, fileParts), nil

	case "raw":
		// /raw/refs/heads/{branch}/{file...}  или  /raw/{branch}/{file...}
		rest := segments[3:]
		if len(rest) == 0 {
			return "", errors.New("raw URL не содержит ветку и файл")
		}
		if len(rest) >= 3 && rest[0] == "refs" && rest[1] == "heads" {
			if len(rest) < 4 {
				return "", errors.New("raw refs/heads URL не содержит путь к файлу")
			}
			return buildRawURL(owner, repo, rest[2], rest[3:]), nil
		}
		// /raw/{branch}/{file...}
		if len(rest) < 2 {
			return "", errors.New("raw URL не содержит путь к файлу")
		}
		return buildRawURL(owner, repo, rest[0], rest[1:]), nil

	default:
		// Нет глагола: github.com/{owner}/{repo}/{file...}
		// Ветка неизвестна — используем "main" по умолчанию.
		fileParts := segments[2:]
		return buildRawURL(owner, repo, "main", fileParts), nil
	}
}

// convertGitVerseURL обрабатывает ссылки с gitverse.ru.
//
// Формат входа:  /content/{branch}/{file...}[?v=...]
// Формат выхода: /api/repos/{owner}/{repo}/raw/branch/{branch}/{file%2Fencoded}
func convertGitVerseURL(segments []string) (string, error) {
	// segments: [owner, repo, "content", branch, file_part...]
	if len(segments) < 5 {
		return "", errors.New("gitverse.ru URL слишком короткий: нужны owner, repo, content, branch и файл")
	}

	owner := segments[0]
	repo := segments[1]
	verb := segments[2]

	if verb != "content" {
		return "", fmt.Errorf("неожиданный глагол в gitverse.ru URL: %q", verb)
	}

	branch := segments[3]
	fileParts := segments[4:]

	// API gitverse.ru требует путь к файлу с %2F вместо /
	encodedPath := strings.Join(fileParts, "%2F")

	return fmt.Sprintf(
		"https://gitverse.ru/api/repos/%s/%s/raw/branch/%s/%s",
		owner, repo, branch, encodedPath,
	), nil
}
