package domain

import (
	"math"
	"testing"
	"time"
)

func TestStorageForecast(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		currentStock int
		periodSales  float64
		pastCost     float64
		windowDays   int
		horizonDays  int
		freeUntil    time.Time
		want         float64
	}{
		{
			// Обратная связь по «Багажник усил. Flame сер.»: товар распродан,
			// прошлое списание 1429 ₽ не должно превращаться в прогноз.
			name:         "распродано — будущих списаний нет",
			currentStock: 0, periodSales: 40, pastCost: 1429,
			windowDays: 28, horizonDays: 30,
			want: 0,
		},
		{
			name:         "нет прошлых списаний — тариф неизвестен",
			currentStock: 50, periodSales: 10, pastCost: 0,
			windowDays: 28, horizonDays: 30,
			want: 0,
		},
		{
			// Тариф: 280 ₽ / (28 дн × 10 шт) = 1 ₽ за экземпляр в день.
			// Товар не продается, значит 10 шт × 30 дн × 1 ₽ = 300 ₽.
			name:         "товар стоит на складе весь горизонт",
			currentStock: 10, periodSales: 0, pastCost: 280,
			windowDays: 28, horizonDays: 30,
			want: 300,
		},
		{
			// Средний остаток за период = 10 + 280/2 = 150, тариф =
			// 1960 / (28 × 150) = 0,4667 ₽. Товар продается по 10 шт в день и
			// уходит за 1 день: 10 экземпляро-дней × 0,4667 = 4,67 ₽.
			name:         "быстрая распродажа уменьшает прогноз",
			currentStock: 10, periodSales: 280, pastCost: 1960,
			windowDays: 28, horizonDays: 30,
			want: 4.67,
		},
		{
			// Бесплатное размещение заканчивается через 10 дней: тарифицируются
			// только оставшиеся 20 дней из 30.
			name:         "дни бесплатного размещения не тарифицируются",
			currentStock: 30, periodSales: 0, pastCost: 840,
			windowDays: 28, horizonDays: 30,
			freeUntil: now.AddDate(0, 0, 10),
			want:      600,
		},
		{
			name:         "бесплатный период закрывает весь горизонт",
			currentStock: 30, periodSales: 0, pastCost: 840,
			windowDays: 28, horizonDays: 30,
			freeUntil: now.AddDate(0, 0, 60),
			want:      0,
		},
		{
			name:         "бесплатный период уже закончился",
			currentStock: 10, periodSales: 0, pastCost: 280,
			windowDays: 28, horizonDays: 30,
			freeUntil: now.AddDate(0, 0, -5),
			want:      300,
		},
	}

	for _, tt := range tests {
		got := StorageForecast(tt.currentStock, tt.periodSales, tt.pastCost, tt.windowDays, tt.horizonDays, tt.freeUntil, now)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("%s: StorageForecast = %.2f, ожидалось %.2f", tt.name, got, tt.want)
		}
	}
}

// Прогноз не должен зависеть от того, в каком часовом поясе считаются даты:
// дата из отчета Ozon приходит без времени.
func TestStorageForecastFreeDaysRounding(t *testing.T) {
	now := time.Date(2026, 10, 18, 23, 30, 0, 0, time.UTC)
	freeUntil := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) // через 30 минут

	got := StorageForecast(10, 0, 280, 28, 30, freeUntil, now)
	// Остается 1 «бесплатный» день из 30, значит тарифицируются 29 дней.
	want := 290.0
	if math.Abs(got-want) > 0.01 {
		t.Errorf("StorageForecast = %.2f, ожидалось %.2f", got, want)
	}
}
