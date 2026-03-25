package model

import (
	"sort"
	"strings"
)

// Normalizer keeps incoming state consistent with the current runtime rules.
type Normalizer struct {
	baseLocale string
	locales    map[string]string
}

func NewNormalizer(baseLocale string, availableLocales map[string]string) Normalizer {
	cloned := make(map[string]string, len(availableLocales))
	for code, name := range availableLocales {
		cloned[code] = name
	}
	return Normalizer{baseLocale: baseLocale, locales: cloned}
}

func (n Normalizer) NormalizeChannels(payload any) []ChannelSubscription {
	items, ok := payload.([]any)
	if !ok {
		return nil
	}

	seen := make(map[int64]ChannelSubscription)
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		id, ok := asInt64(m["id"])
		if !ok {
			continue
		}

		locale := n.normalizeLocaleFromAny(m["locale"])
		if locale == "" {
			locale = n.baseLocale
		}

		seen[id] = ChannelSubscription{
			ID:          id,
			ShownAssets: asBool(m["shown_assets"]),
			Locale:      locale,
		}
	}

	out := make([]ChannelSubscription, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	return out
}

func (n Normalizer) NormalizeGuildConfigs(payload any) []GuildConfig {
	items, ok := payload.([]any)
	if !ok {
		return nil
	}

	seen := make(map[int64]GuildConfig)
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		guildID, ok := asInt64(m["guild_id"])
		if !ok {
			continue
		}

		cfg := GuildConfig{
			GuildID:       guildID,
			ShownAssets:   asBool(m["shown_assets"]),
			Locale:        n.normalizeLocaleFromAny(m["locale"]),
			Enabled:       asBoolDefault(m["enabled"], true),
			IncludeImages: asBoolDefault(m["include_images"], true),
		}
		if cfg.Locale == "" {
			cfg.Locale = n.baseLocale
		}
		if channelID, ok := asInt64Ptr(m["channel_id"]); ok {
			cfg.ChannelID = channelID
		}
		if threadID, ok := asInt64Ptr(m["thread_id"]); ok {
			cfg.ThreadID = threadID
		}
		if roleID, ok := asInt64Ptr(m["mention_role_id"]); ok {
			cfg.MentionRoleID = roleID
		}

		seen[guildID] = cfg
	}

	out := make([]GuildConfig, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	return out
}

func (n Normalizer) NormalizeUserProfiles(payload any) []UserProfile {
	items, ok := payload.([]any)
	if !ok {
		return nil
	}

	seen := make(map[int64]UserProfile)
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		userID, ok := asInt64(m["id"])
		if !ok {
			continue
		}

		locale := n.normalizeLocaleFromAny(m["locale"])
		if locale == "" {
			locale = n.baseLocale
		}

		seen[userID] = UserProfile{
			ID:          userID,
			ShownAssets: asBool(m["shown_assets"]),
			Locale:      locale,
			Subscribed:  asBoolDefault(m["subscribed"], true),
		}
	}

	out := make([]UserProfile, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	return out
}

func (n Normalizer) NormalizeAssets(payload any) []Asset {
	items, ok := payload.([]any)
	if !ok {
		return nil
	}

	out := make([]Asset, 0, len(items))
	seenLinks := make(map[string]struct{})
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}

		link, _ := m["link"].(string)
		link = strings.TrimSpace(link)
		if link == "" {
			continue
		}
		if _, ok := seenLinks[link]; ok {
			continue
		}
		seenLinks[link] = struct{}{}

		asset := Asset{Link: link}
		if name, ok := nonEmptyString(m["name"]); ok {
			asset.Name = &name
		}
		if image, ok := nonEmptyString(m["image"]); ok {
			asset.Image = &image
		}
		out = append(out, asset)
	}

	return out
}

func (n Normalizer) NormalizeDeadline(payload any) StoredDeadline {
	switch v := payload.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return StoredDeadline{}
		}
		return StoredDeadline{Raw: &v}
	case nil:
		return StoredDeadline{}
	case map[string]any:
		info, ok := deadlineFromMap(v)
		if !ok {
			return StoredDeadline{}
		}
		return StoredDeadline{Structured: &info}
	default:
		return StoredDeadline{}
	}
}

func (n Normalizer) normalizeLocaleFromAny(value any) string {
	raw, ok := value.(string)
	if !ok {
		return ""
	}
	return n.NormalizeLocale(raw)
}

// NormalizeLocale returns the canonical locale code or an empty string.
func (n Normalizer) NormalizeLocale(locale string) string {
	candidate := strings.TrimSpace(strings.ReplaceAll(locale, "_", "-"))
	if candidate == "" {
		return ""
	}

	aliases := make(map[string]string, len(n.locales)*4)
	for _, code := range n.sortedLocaleCodes() {
		name := n.locales[code]
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

func (n Normalizer) sortedLocaleCodes() []string {
	codes := make([]string, 0, len(n.locales))
	for code := range n.locales {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func asBool(value any) bool {
	v, ok := value.(bool)
	return ok && v
}

func asBoolDefault(value any, defaultValue bool) bool {
	if v, ok := value.(bool); ok {
		return v
	}
	return defaultValue
}

func asInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), v == float64(int64(v))
	case float32:
		return int64(v), v == float32(int64(v))
	default:
		return 0, false
	}
}

func asInt64Ptr(value any) (*int64, bool) {
	v, ok := asInt64(value)
	if !ok {
		return nil, false
	}
	return &v, true
}

func nonEmptyString(value any) (string, bool) {
	v, ok := value.(string)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

func deadlineFromMap(payload map[string]any) (DeadlineInfo, bool) {
	required := []string{"day", "month", "year", "hour", "minute"}
	info := DeadlineInfo{}
	for _, key := range required {
		raw, ok := payload[key]
		if !ok {
			return DeadlineInfo{}, false
		}
		value, ok := asInt64(raw)
		if !ok {
			return DeadlineInfo{}, false
		}
		switch key {
		case "day":
			info.Day = int(value)
		case "month":
			info.Month = int(value)
		case "year":
			info.Year = int(value)
		case "hour":
			info.Hour = int(value)
		case "minute":
			info.Minute = int(value)
		}
	}

	gmtOffset, ok := payload["gmt_offset"].(string)
	if !ok || strings.TrimSpace(gmtOffset) == "" {
		return DeadlineInfo{}, false
	}
	info.GMTOffset = gmtOffset
	return info, true
}
