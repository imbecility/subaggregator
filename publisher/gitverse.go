package publisher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const gitverseAPIBase = "https://api.gitverse.ru"

var (
	// apiClient — короткие API-вызовы (создание релиза, листинг/удаление ассетов)
	apiClient = &http.Client{Timeout: 30 * time.Second}
	// uploadClient — без жёсткого таймаута: загрузка большого файла
	uploadClient = &http.Client{Timeout: 10 * time.Minute}
)

type releasePayload struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type releaseResponse struct {
	ID int `json:"id"`
}

type assetResponse struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// PublishToGitverse создаёт (или находит существующий) релиз и загружает файл как ассет.
// Если ассет с таким именем уже есть — удаляет его перед загрузкой (аналог --clobber).
//
// ownerRepo — строка вида "owner/repo".
// tag       — тег релиза (например "sub").
// filePath  — локальный путь к файлу для загрузки.
func PublishToGitverse(token, ownerRepo, tag, filePath string) error {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("gitverse-repo должен быть в формате owner/repo, получено: %q", ownerRepo)
	}
	owner, repo := parts[0], parts[1]

	releaseID, err := ensureRelease(token, owner, repo, tag)
	if err != nil {
		return fmt.Errorf("обеспечение релиза: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[INFO] GitVerse: release ID=%d\n", releaseID)

	assetName := filepath.Base(filePath)

	// Удаляем старый ассет — нефатально, предупреждаем и продолжаем
	if err := deleteAssetIfExists(token, owner, repo, releaseID, assetName); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] GitVerse: не удалось удалить старый ассет: %v\n", err)
	}

	if err := uploadAsset(token, owner, repo, releaseID, filePath, assetName); err != nil {
		return fmt.Errorf("загрузка ассета: %w", err)
	}
	return nil
}

// ensureRelease создаёт релиз; при конфликте (409) находит существующий по тегу.
func ensureRelease(token, owner, repo, tag string) (int, error) {
	payload := releasePayload{
		TagName:    tag,
		Name:       "Aggregated Subscription",
		Body:       "Автообновляемая агрегированная подписка. Не редактировать вручную.",
		Draft:      false,
		Prerelease: false,
	}
	bodyBytes, _ := json.Marshal(payload)

	createURL := fmt.Sprintf("%s/repos/%s/%s/releases", gitverseAPIBase, owner, repo)
	req, err := newAPIReq("POST", createURL, token, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := apiClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST releases: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 201:
		var r releaseResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return 0, fmt.Errorf("decode create response: %w", err)
		}
		return r.ID, nil

	case 409:
		// Релиз с таким тегом уже существует — ищем его
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return getReleaseByTag(token, owner, repo, tag)

	default:
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("POST releases: HTTP %d: %s", resp.StatusCode, string(body))
	}
}

func getReleaseByTag(token, owner, repo, tag string) (int, error) {
	getURL := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s",
		gitverseAPIBase, owner, repo, url.PathEscape(tag))
	req, err := newAPIReq("GET", getURL, token, nil)
	if err != nil {
		return 0, err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET release by tag: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("GET release by tag: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var r releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, fmt.Errorf("decode get release: %w", err)
	}
	return r.ID, nil
}

func deleteAssetIfExists(token, owner, repo string, releaseID int, assetName string) error {
	listURL := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets",
		gitverseAPIBase, owner, repo, releaseID)
	req, err := newAPIReq("GET", listURL, token, nil)
	if err != nil {
		return err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET assets: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET assets: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var assets []assetResponse
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return fmt.Errorf("decode assets: %w", err)
	}

	for _, a := range assets {
		if a.Name != assetName {
			continue
		}
		delURL := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets/%d",
			gitverseAPIBase, owner, repo, releaseID, a.ID)
		delReq, err := newAPIReq("DELETE", delURL, token, nil)
		if err != nil {
			return err
		}
		delResp, err := apiClient.Do(delReq)
		if err != nil {
			return fmt.Errorf("DELETE asset: %w", err)
		}
		io.Copy(io.Discard, delResp.Body) //nolint:errcheck
		delResp.Body.Close()
		if delResp.StatusCode != 204 {
			return fmt.Errorf("DELETE asset: HTTP %d", delResp.StatusCode)
		}
		fmt.Fprintf(os.Stderr, "[INFO] GitVerse: удалён старый ассет %q (id=%d)\n", assetName, a.ID)
	}
	return nil
}

// uploadAsset стримит файл через io.Pipe — полный файл никогда не буферизуется в памяти.
func uploadAsset(token, owner, repo string, releaseID int, filePath, assetName string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	// f закрывается внутри горутины

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType() // фиксируем boundary до закрытия writer-а

	go func() {
		defer f.Close()
		fw, err := mw.CreateFormFile("attachment", assetName)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("create form file: %w", err))
			return
		}
		if _, err := io.Copy(fw, f); err != nil {
			pw.CloseWithError(fmt.Errorf("copy file: %w", err))
			return
		}
		mw.Close()
		pw.Close()
	}()

	uploadURL := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets?name=%s",
		gitverseAPIBase, owner, repo, releaseID, url.QueryEscape(assetName))

	req, err := http.NewRequest("POST", uploadURL, pr)
	if err != nil {
		pr.CloseWithError(err) //nolint:errcheck
		return fmt.Errorf("new upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=1")
	req.Header.Set("Content-Type", contentType)

	fmt.Fprintf(os.Stderr, "[INFO] GitVerse: загружаем %s...\n", assetName)
	resp, err := uploadClient.Do(req)
	if err != nil {
		// Закрытие pr сигнализирует горутине об ошибке через io.Pipe
		return fmt.Errorf("POST asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST asset: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func newAPIReq(method, rawURL, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=1")
	return req, nil
}
