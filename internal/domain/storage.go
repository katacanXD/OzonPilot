package domain

import (
	"math"
	"time"
)

// DefaultStorageForecastDays — горизонт прогноза будущих списаний за хранение.
const DefaultStorageForecastDays = 30

// StorageForecast оценивает, сколько Ozon спишет за хранение FBO за
// horizonDays дней вперёд, исходя из текущего остатка на складах.
//
// Причина такого расчета: сумма из отчета Ozon («списано за период») говорит
// о прошлом. Товар мог быть уже распродан, и тогда она показывает деньги,
// которых больше не будет.
//
// Тариф (₽ за экземпляр в день) заранее не известен, поэтому он оценивается по
// прошлому списанию: pastCost за windowDays при среднем остатке за этот период.
// Средний остаток приближается как currentStock + periodSales/2 — запас
// плавно убывал от «сейчас + продажи» до текущего значения.
//
// Прогноз идет по дням: остаток уменьшается на среднесуточные продажи, а дни
// бесплатного размещения (freeUntil) не тарифицируются. Поэтому товар, которого
// уже нет на FBO, дает 0 ₽.
func StorageForecast(
	currentStock int,
	periodSales float64,
	pastCost float64,
	windowDays, horizonDays int,
	freeUntil, now time.Time,
) float64 {
	if currentStock <= 0 || pastCost <= 0 || windowDays <= 0 || horizonDays <= 0 {
		return 0
	}

	avgPastStock := float64(currentStock) + periodSales/2
	if avgPastStock <= 0 {
		return 0
	}
	tariffPerUnitDay := pastCost / (float64(windowDays) * avgPastStock)

	freeDays := daysUntil(freeUntil, now)
	dailySales := periodSales / float64(windowDays)

	// Экземпляро-дни хранения: столько единиц товара будет лежать на складе за
	// горизонт. Быстрая распродажа уменьшает прогноз, нулевые продажи — нет.
	unitDays := 0.0
	for day := 0; day < horizonDays; day++ {
		if day < freeDays {
			continue
		}
		stock := float64(currentStock) - dailySales*float64(day)
		if stock <= 0 {
			break
		}
		unitDays += stock
	}

	return math.Round(tariffPerUnitDay*unitDays*100) / 100
}

// daysUntil возвращает число целых дней до даты; 0 — если даты нет или она прошла.
func daysUntil(date, now time.Time) int {
	if date.IsZero() {
		return 0
	}
	days := int(math.Ceil(date.Sub(now).Hours() / 24))
	if days < 0 {
		return 0
	}
	return days
}
