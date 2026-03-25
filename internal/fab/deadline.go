package fab

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var deadlinePattern = regexp.MustCompile(`(?i)Until\s+([A-Za-z]{3,9})\s+(\d{1,2})(?:,?\s*(\d{4}))?\s+at\s+(\d{1,2}):(\d{2})\s*(AM|PM)?\s*([A-Z]{2,4})`)
var deadlineHeadingPattern = regexp.MustCompile(`(?i)\(([^)]*Until[^)]*)\)`)

var monthMap = map[string]int{
	"jan": 1, "january": 1,
	"feb": 2, "february": 2,
	"mar": 3, "march": 3,
	"apr": 4, "april": 4,
	"may": 5,
	"jun": 6, "june": 6,
	"jul": 7, "july": 7,
	"aug": 8, "august": 8,
	"sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10,
	"nov": 11, "november": 11,
	"dec": 12, "december": 12,
}

var timezoneMap = map[string]string{
	"ET":  "America/New_York",
	"PT":  "America/Los_Angeles",
	"UTC": "UTC",
	"GMT": "UTC",
}

// ParseDeadlineInfo extracts the deadline info from a heading like the Python scraper.
func ParseDeadlineInfo(headingText string) *DeadlineInfo {
	if strings.TrimSpace(headingText) == "" {
		return nil
	}

	normalizedHeading := html.UnescapeString(headingText)
	match := deadlineHeadingPattern.FindStringSubmatch(normalizedHeading)
	if match == nil {
		return nil
	}

	parts := deadlinePattern.FindStringSubmatch(match[1])
	if parts == nil {
		return nil
	}

	monthName := strings.ToLower(parts[1])
	month, ok := monthMap[monthName]
	if !ok {
		return nil
	}

	day, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil
	}

	year := time.Now().Year()
	if parts[3] != "" {
		parsedYear, err := strconv.Atoi(parts[3])
		if err != nil {
			return nil
		}
		year = parsedYear
	}

	hour12, err := strconv.Atoi(parts[4])
	if err != nil {
		return nil
	}

	minute, err := strconv.Atoi(parts[5])
	if err != nil {
		return nil
	}

	meridiem := strings.ToUpper(parts[6])
	timezoneAbbr := strings.ToUpper(parts[7])

	hour := hour12 % 12
	if meridiem == "PM" {
		hour += 12
	}

	locationName, ok := timezoneMap[timezoneAbbr]
	if !ok {
		locationName = "UTC"
	}

	loc, err := time.LoadLocation(locationName)
	if err != nil {
		loc = time.UTC
	}

	localTime := time.Date(year, time.Month(month), day, hour, minute, 0, 0, loc)
	_, offset := localTime.Zone()

	return &DeadlineInfo{
		Day:       day,
		Month:     month,
		Year:      year,
		Hour:      hour,
		Minute:    minute,
		GMTOffset: formatGMTOffset(offset),
	}
}

func formatGMTOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}

	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	if minutes == 0 {
		return fmt.Sprintf("GMT%s%d", sign, hours)
	}
	return fmt.Sprintf("GMT%s%d:%02d", sign, hours, minutes)
}
