package config

import (
	"testing"
	"time"
)

func parseDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("не удалось разобрать дату %q: %v", value, err)
	}
	return parsed
}

func TestParseDaysValue(t *testing.T) {
	now := parseDate(t, "2026-10-22")

	cases := map[string]int{
		"28":      28,
		" 28 ":    28,
		"1":       1,
		"mounth":  30, // 23.09–22.10
		"month":   30,
		"месяц":   30,
		"MOUNTH":  30,
		"quarter": 92, // 23.07–22.10
		"квартал": 92,
	}

	for raw, want := range cases {
		got, err := ParseDaysValue(raw, now)
		if err != nil {
			t.Errorf("ParseDaysValue(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDaysValue(%q) = %d, ожидалось %d", raw, got, want)
		}
	}
}

func TestParseDaysValueErrors(t *testing.T) {
	now := parseDate(t, "2026-10-22")
	for _, raw := range []string{"", "  ", "0", "-3", "28d", "мес"} {
		if _, err := ParseDaysValue(raw, now); err == nil {
			t.Errorf("ParseDaysValue(%q) должен вернуть ошибку", raw)
		}
	}
}

// Конец месяца не должен «переползать» в следующий месяц: 31 марта минус
// месяц — это 28 февраля, плюс день = 1 марта, итого 31 день окна.
func TestParseDaysValueClampsMonthEnd(t *testing.T) {
	cases := map[string]int{
		"2026-03-31": 31,
		"2028-03-31": 31, // високосный год: 29 февраля + 1 день
		"2026-01-31": 31,
		"2026-05-31": 31,
	}
	for raw, want := range cases {
		got, err := ParseDaysValue("mounth", parseDate(t, raw))
		if err != nil {
			t.Errorf("ParseDaysValue(mounth, %s): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("окно на %s = %d дней, ожидалось %d", raw, got, want)
		}
	}
}
