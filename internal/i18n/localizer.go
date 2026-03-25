package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Localizer resolves localized strings from JSON catalogs on disk.
type Localizer struct {
	locale        string
	defaultLocale string
	localesDir    string
	catalogCache  map[string]map[string]any
	nameCache     map[string]string
}

// New builds a localizer and eagerly loads the default catalog.
func New(locale, defaultLocale, localesDir string) (*Localizer, error) {
	l := &Localizer{
		locale:        locale,
		defaultLocale: defaultLocale,
		localesDir:    localesDir,
		catalogCache:  make(map[string]map[string]any),
		nameCache:     make(map[string]string),
	}
	if _, err := l.loadCatalog(defaultLocale, true); err != nil {
		return nil, err
	}
	_, _ = l.loadCatalog(locale, false)
	return l, nil
}

func (l *Localizer) Locale() string {
	return l.locale
}

func (l *Localizer) DefaultLocale() string {
	return l.defaultLocale
}

func (l *Localizer) T(key string, args map[string]any) string {
	return l.translate(key, args)
}

func (l *Localizer) MonthName(month int, context string) string {
	return l.T(fmt.Sprintf("calendar.months.%s.wide.%d", context, month), nil)
}

func (l *Localizer) ForLocale(locale string) *Localizer {
	resolved := l.NormalizeLocale(locale)
	if resolved == "" {
		resolved = l.defaultLocale
	}
	if resolved == l.locale {
		return l
	}
	return &Localizer{
		locale:        resolved,
		defaultLocale: l.defaultLocale,
		localesDir:    l.localesDir,
		catalogCache:  l.catalogCache,
		nameCache:     l.nameCache,
	}
}

func (l *Localizer) AvailableLocales() map[string]string {
	codes := l.sortedLocaleCodes()
	if len(codes) == 0 {
		return map[string]string{}
	}

	out := make(map[string]string, len(codes))
	for _, code := range codes {
		out[code] = l.catalogLocaleName(code)
	}
	return out
}

func (l *Localizer) LocaleName(locale string) string {
	resolved := l.NormalizeLocale(locale)
	if resolved == "" {
		resolved = strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))
	}
	return l.catalogLocaleName(resolved)
}

func (l *Localizer) NormalizeLocale(locale string) string {
	candidate := strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))
	if candidate == "" {
		return ""
	}

	aliases := make(map[string]string)
	for _, code := range l.sortedLocaleCodes() {
		name := l.catalogLocaleName(code)
		normalizedCode := strings.ToLower(code)
		aliases[normalizedCode] = code
		aliases[strings.ReplaceAll(normalizedCode, "-", "")] = code

		short := strings.ToLower(strings.SplitN(code, "-", 2)[0])
		if _, ok := aliases[short]; !ok {
			aliases[short] = code
		}

		normalizedName := strings.ToLower(strings.TrimSpace(name))
		if normalizedName != "" {
			aliases[normalizedName] = code
		}
	}

	return aliases[strings.ToLower(candidate)]
}

func (l *Localizer) translate(key string, args map[string]any) string {
	template := l.lookup(l.catalogFor(l.locale), key)
	if template == nil {
		template = l.lookup(l.catalogFor(l.defaultLocale), key)
	}
	if template == nil {
		return key
	}

	s, ok := template.(string)
	if !ok {
		return key
	}
	return formatTemplate(s, args)
}

func (l *Localizer) loadCatalog(locale string, required bool) (map[string]any, error) {
	if catalog, ok := l.catalogCache[locale]; ok {
		return catalog, nil
	}

	path := filepath.Join(l.localesDir, locale+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		if required {
			return nil, fmt.Errorf("locale catalog not found: %s", path)
		}
		return map[string]any{}, nil
	}
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})

	var catalog map[string]any
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("locale catalog must contain a JSON object")
	}
	l.catalogCache[locale] = catalog
	return catalog, nil
}

func (l *Localizer) catalogFor(locale string) map[string]any {
	catalog, err := l.loadCatalog(locale, false)
	if err != nil {
		return map[string]any{}
	}
	return catalog
}

func (l *Localizer) catalogLocaleName(locale string) string {
	if cached, ok := l.nameCache[locale]; ok {
		return cached
	}

	name := locale
	catalog, err := l.loadCatalog(locale, false)
	if err == nil {
		if meta, ok := catalog["meta"].(map[string]any); ok {
			if raw, ok := meta["name"].(string); ok {
				raw = strings.TrimSpace(raw)
				if raw != "" {
					name = raw
				}
			}
		}
	}

	l.nameCache[locale] = name
	return name
}

func (l *Localizer) sortedLocaleCodes() []string {
	entries, err := os.ReadDir(l.localesDir)
	if err != nil {
		return nil
	}

	codes := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		codes = append(codes, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(codes)
	return codes
}

func (l *Localizer) lookup(catalog map[string]any, key string) any {
	var current any = catalog
	for _, part := range strings.Split(key, ".") {
		next, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = next[part]
	}
	return current
}

func formatTemplate(template string, args map[string]any) string {
	if len(args) == 0 {
		return template
	}

	var b strings.Builder
	for i := 0; i < len(template); i++ {
		if template[i] != '{' {
			b.WriteByte(template[i])
			continue
		}

		end := strings.IndexByte(template[i+1:], '}')
		if end < 0 {
			b.WriteByte(template[i])
			continue
		}
		expr := template[i+1 : i+1+end]
		key, formatSpec := splitFormat(expr)
		value, ok := args[key]
		if !ok {
			b.WriteString(template[i : i+end+2])
			i += end + 1
			continue
		}

		b.WriteString(applyFormat(value, formatSpec))
		i += end + 1
	}
	return b.String()
}

func splitFormat(expr string) (string, string) {
	if idx := strings.IndexByte(expr, ':'); idx >= 0 {
		return expr[:idx], expr[idx+1:]
	}
	return expr, ""
}

func applyFormat(value any, spec string) string {
	if spec == "" {
		return fmt.Sprint(value)
	}

	switch v := value.(type) {
	case int:
		return formatInt(int64(v), spec)
	case int8:
		return formatInt(int64(v), spec)
	case int16:
		return formatInt(int64(v), spec)
	case int32:
		return formatInt(int64(v), spec)
	case int64:
		return formatInt(v, spec)
	case float64:
		return formatInt(int64(v), spec)
	case float32:
		return formatInt(int64(v), spec)
	default:
		return fmt.Sprint(value)
	}
}

func formatInt(v int64, spec string) string {
	switch spec {
	case "02", "02d":
		return fmt.Sprintf("%02d", v)
	default:
		return strconv.FormatInt(v, 10)
	}
}
