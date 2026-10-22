package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Ключевые слова для флагов с количеством дней: их можно писать вместо числа.
const (
	keywordMonth     = "mounth"
	keywordMonth2    = "month"
	keywordMonthRu   = "месяц"
	keywordQuarter   = "quarter"
	keywordQuarterRu = "квартал"
)

// ParseDaysValue превращает значение флага в число дней окна.
//
// Принимает число дней ("28") или ключевое слово:
//   - mounth / month / месяц — от сегодня назад на 1 месяц, плюс один день;
//   - quarter / квартал — то же самое, но назад на 3 месяца.
//
// Для сегодняшнего 22.10 mounth даёт окно 23.09–22.10, то есть 30 дней.
func ParseDaysValue(raw string, now time.Time) (int, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 0, fmt.Errorf("пустое значение: укажите число дней или mounth/quarter")
	}

	switch value {
	case keywordMonth, keywordMonth2, keywordMonthRu:
		return windowDaysForMonths(now, 1), nil
	case keywordQuarter, keywordQuarterRu:
		return windowDaysForMonths(now, 3), nil
	}

	days, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("не удалось разобрать %q: нужно число дней или mounth/quarter", raw)
	}
	if days <= 0 {
		return 0, fmt.Errorf("количество дней должно быть больше 0, получено %d", days)
	}
	return days, nil
}

// windowDaysForMonths считает длину окна «назад на N месяцев, плюс один день»
// включительно по сегодняшний день. Получается столько же дней, сколько
// показывает кабинет Ozon за такой же период.
func windowDaysForMonths(now time.Time, months int) int {
	end := dateOnly(now)
	start := addMonthsClamped(end, -months).AddDate(0, 0, 1)
	return daysBetweenInclusive(start, end)
}

// addMonthsClamped сдвигает дату на месяцы, не выходя за конец месяца:
// 31 марта минус месяц — это 28/29 февраля, а не 3 марта.
func addMonthsClamped(t time.Time, months int) time.Time {
	year, month, day := t.Date()
	shifted := time.Date(year, month+time.Month(months), 1, 0, 0, 0, 0, t.Location())
	lastDay := time.Date(shifted.Year(), shifted.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(shifted.Year(), shifted.Month(), day, 0, 0, 0, 0, t.Location())
}

// daysBetweenInclusive считает дни от start до end включительно.
func daysBetweenInclusive(start, end time.Time) int {
	days := int(dateOnly(end).Sub(dateOnly(start)).Hours() / 24)
	if days < 0 {
		days = -days
	}
	return days + 1
}

// dateOnly отбрасывает время: окно считается по календарным дням.
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
