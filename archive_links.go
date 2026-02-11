package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	waybackAPI = "https://web.archive.org/web"
	userAgent  = "Mozilla/5.0 (compatible; BookmarkArchiver/1.0)"
)

func defaultBookmarksDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home cannot be determined
		return "pinboard-bookmarks"
	}
	return filepath.Join(home, "pinboard-bookmarks")
}

type BookmarkFile struct {
	Path    string
	Link    string
	Date    string
	Content string
	Headers map[string]string
}

type LockFile struct {
	ProcessedFiles map[string]string `json:"processed_files"` // path -> hash
	LastRun        time.Time         `json:"last_run"`
}

func getLockFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".archive_tool.lock"
	}
	return filepath.Join(home, ".archive_tool.lock")
}

func loadLockFile() (*LockFile, error) {
	lockPath := getLockFilePath()
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &LockFile{
				ProcessedFiles: make(map[string]string),
				LastRun:        time.Now(),
			}, nil
		}
		return nil, err
	}

	var lock LockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return &LockFile{
			ProcessedFiles: make(map[string]string),
			LastRun:        time.Now(),
		}, nil
	}

	if lock.ProcessedFiles == nil {
		lock.ProcessedFiles = make(map[string]string)
	}

	return &lock, nil
}

func saveLockFile(lock *LockFile) error {
	lockPath := getLockFilePath()
	lock.LastRun = time.Now()

	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(lockPath, data, 0644)
}

func computeFileHash(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

func isFileProcessed(lock *LockFile, filePath string) bool {
	currentHash, err := computeFileHash(filePath)
	if err != nil {
		return false
	}

	storedHash, exists := lock.ProcessedFiles[filePath]
	return exists && storedHash == currentHash
}

func markFileProcessed(lock *LockFile, filePath string) error {
	hash, err := computeFileHash(filePath)
	if err != nil {
		return err
	}
	lock.ProcessedFiles[filePath] = hash
	return nil
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Usage: archive_tool [directory]")
		fmt.Println("")
		fmt.Println("A tool to check bookmark files for dead links and replace them with archived versions.")
		fmt.Println("")
		fmt.Println("Arguments:")
		fmt.Println("  directory   Path to directory containing bookmark markdown files")
		fmt.Println("              (default: ~/pinboard-bookmarks)")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  archive_tool                    # Use default ~/pinboard-bookmarks")
		fmt.Println("  archive_tool ./my-bookmarks     # Use custom directory")
		os.Exit(0)
	}

	dir := defaultBookmarksDir()
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	fmt.Printf("Scanning directory: %s\n", dir)

	files, err := findMarkdownFiles(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading directory: %v\n", err)
		os.Exit(1)
	}

	lock, err := loadLockFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading lock file: %v\n", err)
		os.Exit(1)
	}

	var unprocessedFiles []string
	for _, filePath := range files {
		if !isFileProcessed(lock, filePath) {
			unprocessedFiles = append(unprocessedFiles, filePath)
		}
	}

	skipped := len(files) - len(unprocessedFiles)
	fmt.Printf("Found %d markdown files (%d already processed, %d new)\n", len(files), skipped, len(unprocessedFiles))

	if len(unprocessedFiles) == 0 {
		fmt.Println("All files have been processed. Nothing to do.")
		os.Exit(0)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	replaced := 0
	checked := 0
	errors := 0

	for i, filePath := range unprocessedFiles {
		fmt.Printf("\rProcessing [%d/%d] - Checked: %d, 404s found: %d, Replaced: %d, Errors: %d",
			i+1, len(unprocessedFiles), checked, replaced, replaced, errors)

		bookmark, err := parseBookmarkFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError parsing %s: %v\n", filePath, err)
			errors++
			continue
		}

		if bookmark.Link == "" {
			markFileProcessed(lock, filePath)
			continue
		}

		checked++

		is404, err := checkURL(client, bookmark.Link)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError checking %s: %v\n", bookmark.Link, err)
			errors++
			continue
		}

		if !is404 {
			markFileProcessed(lock, filePath)
			continue
		}

		archivedURL, err := findArchivedVersion(client, bookmark.Link, bookmark.Date)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError finding archive for %s: %v\n", bookmark.Link, err)
			errors++
			continue
		}

		if archivedURL == "" {
			fmt.Printf("\nNo archive found for: %s\n", bookmark.Link)
			markFileProcessed(lock, filePath)
			continue
		}

		err = updateBookmarkFile(bookmark, archivedURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError updating %s: %v\n", filePath, err)
			errors++
			continue
		}

		markFileProcessed(lock, filePath)
		replaced++
		fmt.Printf("\n✓ Replaced: %s\n  -> %s\n", bookmark.Link, archivedURL)
	}

	if err := saveLockFile(lock); err != nil {
		fmt.Fprintf(os.Stderr, "\nError saving lock file: %v\n", err)
	}

	fmt.Printf("\n\nDone! Checked: %d, Replaced: %d, Errors: %d, Skipped: %d\n", checked, replaced, errors, skipped)
}

func findMarkdownFiles(dir string) ([]string, error) {
	var files []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".md") {
			files = append(files, path)
		}
		return nil
	})

	return files, err
}

func parseBookmarkFile(filePath string) (*BookmarkFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	content := string(data)
	bookmark := &BookmarkFile{
		Path:    filePath,
		Content: content,
		Headers: make(map[string]string),
	}

	// Parse YAML frontmatter
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	frontmatterEnd := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			} else {
				frontmatterEnd = i
				break
			}
		}

		if inFrontmatter {
			if strings.HasPrefix(line, "link:") {
				bookmark.Link = extractYAMLValue(line)
			} else if strings.HasPrefix(line, "date:") {
				bookmark.Date = extractYAMLValue(line)
			}

			// Store all headers for reconstruction
			if idx := strings.Index(line, ":"); idx > 0 {
				key := strings.TrimSpace(line[:idx])
				bookmark.Headers[key] = line
			}
		}
	}

	bookmark.Content = strings.Join(lines[frontmatterEnd+1:], "\n")

	return bookmark, nil
}

func extractYAMLValue(line string) string {
	idx := strings.Index(line, ":")
	if idx == -1 {
		return ""
	}

	value := strings.TrimSpace(line[idx+1:])
	// Remove quotes if present
	value = strings.Trim(value, `"'`)

	return value
}

func checkURL(client *http.Client, urlStr string) (bool, error) {
	req, err := http.NewRequest("HEAD", urlStr, nil)
	if err != nil {
		return false, err
	}

	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		// If we can't connect, treat as 404
		return true, nil
	}
	defer resp.Body.Close()

	// Consider 404 and 410 as "not found"
	return resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone, nil
}

func findArchivedVersion(client *http.Client, originalURL, bookmarkDate string) (string, error) {
	// Parse the bookmark date to get a timestamp
	timestamp := parseDateToTimestamp(bookmarkDate)

	// Try to find snapshot near the bookmark date
	// Format: https://web.archive.org/web/<timestamp>/<url>
	// Using the closest snapshot with availability check

	// First, check availability API
	availabilityURL := fmt.Sprintf("%s/%s/%s", waybackAPI, timestamp, originalURL)

	req, err := http.NewRequest("HEAD", availabilityURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return resp.Request.URL.String(), nil
	}

	// If that didn't work, try without timestamp to get any available snapshot
	anySnapshotURL := fmt.Sprintf("%s/*/%s", waybackAPI, originalURL)

	req2, err := http.NewRequest("HEAD", anySnapshotURL, nil)
	if err != nil {
		return "", err
	}

	req2.Header.Set("User-Agent", userAgent)

	resp2, err := client.Do(req2)
	if err != nil {
		return "", err
	}
	resp2.Body.Close()

	if resp2.StatusCode == http.StatusOK {
		// Extract the actual snapshot URL from the redirect
		return resp2.Request.URL.String(), nil
	}

	return "", nil
}

func parseDateToTimestamp(dateStr string) string {
	if dateStr == "" {
		// Default to 6 months ago if no date
		return time.Now().AddDate(0, -6, 0).Format("20060102")
	}

	// Try different date formats
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"January 2, 2006",
		"Jan 2, 2006",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t.Format("20060102")
		}
	}

	// If parsing fails, return current date minus 6 months
	return time.Now().AddDate(0, -6, 0).Format("20060102")
}

func updateBookmarkFile(bookmark *BookmarkFile, newURL string) error {
	data, err := os.ReadFile(bookmark.Path)
	if err != nil {
		return err
	}

	content := string(data)

	// Replace the link in the YAML frontmatter
	oldLinkPattern := regexp.MustCompile(`(link:\s*["']?)` + regexp.QuoteMeta(bookmark.Link) + `(["']?\s*)`)
	newContent := oldLinkPattern.ReplaceAllString(content, "${1}"+newURL+"${2}")

	if newContent == content {
		// If regex didn't match, try simpler string replacement
		newContent = strings.Replace(content, bookmark.Link, newURL, 1)
	}

	return os.WriteFile(bookmark.Path, []byte(newContent), 0644)
}

func extractMainContent(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontmatter := false
	frontmatterClosed := false
	var content []string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			} else if !frontmatterClosed {
				frontmatterClosed = true
				continue
			}
		}

		if frontmatterClosed {
			content = append(content, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return strings.Join(content, "\n"), nil
}
