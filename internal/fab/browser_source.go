package fab

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

const browserOverrideEnv = "ASSETS_BOT_BROWSER"

const defaultBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:144.0) Gecko/20100101 Firefox/144.0"

const antiBotScript = `Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
Object.defineProperty(navigator, 'platform', {get: () => 'Win32'});
Object.defineProperty(navigator, 'language', {get: () => 'en-US'});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});`

type BrowserSource struct {
	BrowserPath   string
	URL           string
	UserAgent     string
	Timeout       time.Duration
	PostLoadDelay time.Duration

	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser
}

func NewBrowserSource() (*BrowserSource, error) {
	browserPath, err := resolveBrowserOverride()
	if err != nil {
		return nil, err
	}

	return &BrowserSource{
		BrowserPath:   browserPath,
		URL:           defaultHomepageURL,
		UserAgent:     defaultBrowserUserAgent,
		Timeout:       60 * time.Second,
		PostLoadDelay: 1500 * time.Millisecond,
	}, nil
}

func (s *BrowserSource) Fetch(ctx context.Context) (document string, err error) {
	if s == nil {
		return "", fmt.Errorf("fab: browser source is not initialized")
	}

	browserPath := strings.TrimSpace(s.BrowserPath)
	if browserPath == "" {
		resolved, resolveErr := resolveBrowserOverride()
		if resolveErr != nil {
			return "", resolveErr
		}
		browserPath = resolved
	}

	url := strings.TrimSpace(s.URL)
	if url == "" {
		url = defaultHomepageURL
	}

	userAgent := strings.TrimSpace(s.UserAgent)
	if userAgent == "" {
		userAgent = defaultBrowserUserAgent
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	postLoadDelay := s.PostLoadDelay
	if postLoadDelay <= 0 {
		postLoadDelay = 1500 * time.Millisecond
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	browser, err := s.ensureBrowserLocked(browserPath)
	if err != nil {
		return "", err
	}

	browserContext, err := browser.NewContext(playwright.BrowserNewContextOptions{
		Locale:    playwright.String("en-US"),
		UserAgent: playwright.String(userAgent),
		Viewport: &playwright.Size{
			Width:  1440,
			Height: 900,
		},
	})
	if err != nil {
		s.resetLocked()
		return "", fmt.Errorf("fab: create firefox context: %w", err)
	}
	defer func() {
		if closeErr := browserContext.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("fab: close firefox context: %w", closeErr)
		}
	}()

	if err := browserContext.AddInitScript(playwright.Script{
		Content: playwright.String(antiBotScript),
	}); err != nil {
		s.resetLocked()
		return "", fmt.Errorf("fab: add anti-bot init script: %w", err)
	}

	page, err := browserContext.NewPage()
	if err != nil {
		s.resetLocked()
		return "", fmt.Errorf("fab: create firefox page: %w", err)
	}

	log.Printf("Loading homepage...")
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		Timeout:   playwright.Float(float64(timeout.Milliseconds())),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		s.resetLocked()
		return "", fmt.Errorf("fab: navigate homepage: %w", err)
	}

	if err := sleepContext(ctx, postLoadDelay); err != nil {
		return "", err
	}

	document, err = page.Content()
	if err != nil {
		s.resetLocked()
		return "", fmt.Errorf("fab: read homepage HTML: %w", err)
	}
	if strings.TrimSpace(document) == "" {
		return "", fmt.Errorf("fab: browser fetch returned empty document")
	}

	return document, nil
}

func (s *BrowserSource) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var result error
	if s.browser != nil {
		if err := s.browser.Close(); err != nil && result == nil {
			result = fmt.Errorf("fab: close firefox: %w", err)
		}
		s.browser = nil
	}
	if s.pw != nil {
		if err := s.pw.Stop(); err != nil && result == nil {
			result = fmt.Errorf("fab: stop playwright: %w", err)
		}
		s.pw = nil
	}
	return result
}

func (s *BrowserSource) ensureBrowserLocked(browserPath string) (playwright.Browser, error) {
	if s.browser != nil {
		return s.browser, nil
	}

	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("fab: start playwright: %w", err)
	}

	launchOptions := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	}
	if browserPath != "" {
		launchOptions.ExecutablePath = playwright.String(browserPath)
	}

	browser, err := pw.Firefox.Launch(launchOptions)
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("fab: launch firefox: %w", err)
	}

	s.pw = pw
	s.browser = browser
	return browser, nil
}

func (s *BrowserSource) resetLocked() {
	if s.browser != nil {
		_ = s.browser.Close()
		s.browser = nil
	}
	if s.pw != nil {
		_ = s.pw.Stop()
		s.pw = nil
	}
}

func resolveBrowserOverride() (string, error) {
	override := strings.TrimSpace(os.Getenv(browserOverrideEnv))
	if override == "" {
		return "", nil
	}

	if resolved, ok := resolveExecutable(override); ok {
		return resolved, nil
	}

	return "", fmt.Errorf("fab: firefox executable %q from %s was not found", override, browserOverrideEnv)
}

func resolveExecutable(candidate string) (string, bool) {
	if strings.TrimSpace(candidate) == "" {
		return "", false
	}

	if strings.Contains(candidate, string(os.PathSeparator)) {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		return "", false
	}

	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", false
	}
	return resolved, true
}
