package fab

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const defaultHomepageURL = "https://www.fab.com/"

var ErrLimitedTimeFreeNotFound = errors.New("fab: limited-time free section not found")

// HTMLSource returns a rendered HTML document.
//
// This package keeps parsing separate from transport so browser-backed and
// non-browser sources can be swapped without touching the retry or parser logic.
type HTMLSource interface {
	Fetch(ctx context.Context) (string, error)
}

// HTTPSource is a minimal transport that fetches the raw homepage over HTTP.
// It is suitable for wiring and tests, but it does not execute client-side JS.
type HTTPSource struct {
	Client *http.Client
	URL    string
}

func (s HTTPSource) Fetch(ctx context.Context) (string, error) {
	url := s.URL
	if url == "" {
		url = defaultHomepageURL
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fab: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// Scraper retries fetching and parsing until the Limited-Time Free section is found.
type Scraper struct {
	Source  HTMLSource
	Retries int
	Backoff func(attempt int) time.Duration
}

func NewScraper(source HTMLSource) *Scraper {
	return &Scraper{
		Source:  source,
		Retries: 5,
		Backoff: defaultBackoff,
	}
}

func defaultBackoff(attempt int) time.Duration {
	_ = attempt
	return 5 * time.Second
}

// GetFreeAssets returns the parsed assets and deadline.
//
// If the section exists but is empty, the returned assets slice is empty and the
// error is nil. If the section cannot be found after retries, ErrLimitedTimeFreeNotFound
// is returned.
func (s *Scraper) GetFreeAssets(ctx context.Context) ([]Asset, *DeadlineInfo, error) {
	if s == nil || s.Source == nil {
		return nil, nil, errors.New("fab: missing HTML source")
	}

	retries := s.Retries
	if retries <= 0 {
		retries = 1
	}

	for attempt := 1; attempt <= retries; attempt++ {
		htmlDoc, err := s.fetchDocument(ctx)
		if err != nil {
			if attempt == retries {
				log.Printf("Failed to fetch Limited-Time Free assets after several attempts.")
				return nil, nil, err
			}
			log.Printf("Homepage parse error: %v. Retrying %d/%d...", err, attempt, retries)
			if sleep := s.backoffDuration(attempt); sleep > 0 {
				if err := sleepContext(ctx, sleep); err != nil {
					return nil, nil, err
				}
			}
			continue
		}

		result, err := ParseFreeAssets(htmlDoc)
		if err != nil {
			if attempt == retries {
				log.Printf("Failed to fetch Limited-Time Free assets after several attempts.")
				return nil, nil, err
			}
			log.Printf("Homepage parse error: %v. Retrying %d/%d...", err, attempt, retries)
			if sleep := s.backoffDuration(attempt); sleep > 0 {
				if err := sleepContext(ctx, sleep); err != nil {
					return nil, nil, err
				}
			}
			continue
		}

		if result.Found {
			if len(result.Assets) == 0 {
				log.Printf("Limited-Time Free section is empty on homepage.")
			} else {
				log.Printf("Collected %d listing cards from homepage.", len(result.Assets))
			}
			return result.Assets, result.Deadline, nil
		}

		if attempt == retries {
			log.Printf("Failed to fetch Limited-Time Free assets after several attempts.")
			return nil, nil, ErrLimitedTimeFreeNotFound
		}
		log.Printf("'Limited-Time Free' section not found. Attempt %d/%d", attempt, retries)
		if sleep := s.backoffDuration(attempt); sleep > 0 {
			if err := sleepContext(ctx, sleep); err != nil {
				return nil, nil, err
			}
		}
	}

	return nil, nil, ErrLimitedTimeFreeNotFound
}

// GetFreeAssets is the package-level convenience entrypoint that matches the
// integration shape requested by the bot runtime.
func GetFreeAssets(ctx context.Context, retries int) ([]Asset, *DeadlineInfo, error) {
	source, err := NewBrowserSource()
	if err != nil {
		return nil, nil, err
	}
	return NewScraper(source).WithRetries(retries).GetFreeAssets(ctx)
}

func (s *Scraper) WithRetries(retries int) *Scraper {
	if s == nil {
		return nil
	}
	s.Retries = retries
	return s
}

// fetchDocument is the only place that talks to the transport layer.
// A browser-backed source can be injected through HTMLSource without touching
// the parser or retry logic.
func (s *Scraper) fetchDocument(ctx context.Context) (string, error) {
	return s.Source.Fetch(ctx)
}

func (s *Scraper) backoffDuration(attempt int) time.Duration {
	if s.Backoff == nil {
		return defaultBackoff(attempt)
	}
	return s.Backoff(attempt)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ParseFreeAssets extracts the Limited-Time Free section from a rendered HTML document.
func ParseFreeAssets(document string) (Result, error) {
	sectionHTML, headingText, found := locateFreeSection(document)
	if !found {
		return Result{}, nil
	}

	assets := parseAssets(sectionHTML)
	if assets == nil {
		assets = []Asset{}
	}

	return Result{
		Assets:   assets,
		Deadline: ParseDeadlineInfo(headingText),
		Found:    true,
	}, nil
}

func locateFreeSection(document string) (sectionHTML string, headingText string, found bool) {
	type sectionFrame struct {
		start int
	}

	tagPattern := regexp.MustCompile(`(?is)<\s*(/?)\s*(section|h2)\b[^>]*>`)

	var sectionStack []sectionFrame
	var inH2 bool
	var h2Text strings.Builder
	var targetStart = -1

	lastEnd := 0
	matches := tagPattern.FindAllStringSubmatchIndex(document, -1)
	for _, match := range matches {
		start := match[0]
		end := match[1]
		isClosing := match[2] != -1 && document[match[2]:match[3]] != ""
		tagName := strings.ToLower(document[match[4]:match[5]])

		if inH2 && start > lastEnd {
			h2Text.WriteString(document[lastEnd:start])
		}

		switch tagName {
		case "section":
			if !isClosing {
				sectionStack = append(sectionStack, sectionFrame{start: start})
			} else if len(sectionStack) > 0 {
				top := sectionStack[len(sectionStack)-1]
				sectionStack = sectionStack[:len(sectionStack)-1]
				if targetStart >= 0 && top.start == targetStart {
					return document[targetStart:end], headingText, true
				}
			}
		case "h2":
			if !isClosing {
				inH2 = true
				h2Text.Reset()
			} else if inH2 {
				cleaned := cleanText(h2Text.String())
				if strings.HasPrefix(cleaned, "Limited-Time Free") && len(sectionStack) > 0 && targetStart < 0 {
					targetStart = sectionStack[len(sectionStack)-1].start
					headingText = cleaned
				}
				inH2 = false
			}
		}

		lastEnd = end
	}

	return "", "", false
}

func parseAssets(sectionHTML string) []Asset {
	if sectionHTML == "" {
		return nil
	}

	liPattern := regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	anchorPattern := regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	imagePattern := regexp.MustCompile(`(?is)<img\b[^>]*src\s*=\s*["']([^"']+)["'][^>]*>`)
	attributePattern := regexp.MustCompile(`(?is)\b([a-zA-Z:-]+)\s*=\s*["']([^"']*)["']`)

	assets := make([]Asset, 0)
	seenLinks := make(map[string]struct{})

	for _, liMatch := range liPattern.FindAllStringSubmatch(sectionHTML, -1) {
		liHTML := liMatch[1]
		anchorMatch := anchorPattern.FindStringSubmatch(liHTML)
		if anchorMatch == nil {
			continue
		}

		attrs := parseAttributes(anchorMatch[1], attributePattern)
		href := strings.TrimSpace(html.UnescapeString(attrs["href"]))
		if !strings.HasPrefix(href, "/listings/") {
			continue
		}

		link := "https://www.fab.com" + href
		if _, exists := seenLinks[link]; exists {
			continue
		}
		seenLinks[link] = struct{}{}

		name := cleanText(stripTags(anchorMatch[2]))
		if name == "" {
			if ariaLabel := attrs["aria-label"]; ariaLabel != "" {
				name = cleanText(ariaLabel)
			}
		}

		image := ""
		if imageMatch := imagePattern.FindStringSubmatch(liHTML); imageMatch != nil {
			image = normalizeImageURL(strings.TrimSpace(html.UnescapeString(imageMatch[1])))
		}

		assets = append(assets, Asset{
			Name:  emptyToZero(name),
			Link:  link,
			Image: emptyToZero(image),
		})
	}

	return assets
}

func parseAttributes(input string, pattern *regexp.Regexp) map[string]string {
	attributes := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(input, -1) {
		if len(match) < 3 {
			continue
		}
		attributes[strings.ToLower(match[1])] = html.UnescapeString(match[2])
	}
	return attributes
}

func normalizeImageURL(url string) string {
	if url == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(url, "//"):
		return "https:" + url
	case strings.HasPrefix(url, "/"):
		return "https://www.fab.com" + url
	default:
		return url
	}
}

func stripTags(input string) string {
	tagPattern := regexp.MustCompile(`(?is)<[^>]+>`)
	return html.UnescapeString(tagPattern.ReplaceAllString(input, ""))
}

func cleanText(input string) string {
	return strings.Join(strings.Fields(stripTags(input)), " ")
}

func emptyToZero(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	return input
}
