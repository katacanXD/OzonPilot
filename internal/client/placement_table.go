package client

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// placementColumns — индексы колонок отчета о размещении. -1 означает, что
// колонки в файле нет.
type placementColumns struct {
	offer, sku, status, date, cost int
	paidQty, totalQty              int
	forecast28, forecastMonth      int
	stock, freeStock, forcedUnits  int
}

// findPlacementHeader ищет строку заголовков. Ozon отдает отчет в двух видах:
// файл API (одна строка заголовков) и выгрузка личного кабинета «Стоимость
// размещения по товарам» (две строки: группы колонок и подзаголовки).
func findPlacementHeader(rows [][]string) (start, end int) {
	for r := 0; r < len(rows) && r < 5; r++ {
		joined := normalizeHeaderCell(strings.Join(rows[r], " "))
		if !strings.Contains(joined, "артикул") && !strings.Contains(joined, "offer_id") && !strings.Contains(joined, "offer") {
			continue
		}
		start, end = r, r
		if r+1 < len(rows) && isPlacementSubHeader(rows[r+1]) {
			end = r + 1
		}
		return start, end
	}
	return -1, -1
}

// isPlacementSubHeader распознает вторую строку заголовков: в ней нет
// артикулов, зато есть названия подколонок.
func isPlacementSubHeader(row []string) bool {
	words := 0
	for _, cell := range row {
		h := normalizeHeaderCell(cell)
		if h == "" {
			continue
		}
		if containsAnyHeader(h, "списан", "остаток", "экземпляр", "денег", "прогноз", "доля", "транзит", "зарезерв", "вынужд", "просмотр", "конверсия", "дней", "индекс", "признак", "рекоменд") {
			words++
		}
	}
	return words >= 2
}

func normalizeHeaderCell(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func containsAnyHeader(h string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(h, part) {
			return true
		}
	}
	return false
}

// combinedHeader склеивает все строки заголовка по колонке: у выгрузки ЛК
// название колонки может быть в первой строке, а подзаголовок — во второй.
func combinedHeader(rows [][]string, start, end, col int) string {
	parts := make([]string, 0, 2)
	for r := start; r <= end; r++ {
		if v := cellAt(rows[r], col); v != "" {
			parts = append(parts, v)
		}
	}
	return normalizeHeaderCell(strings.Join(parts, " "))
}

// cellAt возвращает значение ячейки; col = -1 означает, что колонки нет.
func cellAt(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func detectPlacementColumns(rows [][]string, start, end int) placementColumns {
	cols := placementColumns{
		offer: -1, sku: -1, status: -1, date: -1, cost: -1,
		paidQty: -1, totalQty: -1, forecast28: -1, forecastMonth: -1,
		stock: -1, freeStock: -1, forcedUnits: -1,
	}

	maxCol := 0
	for r := start; r <= end; r++ {
		if len(rows[r]) > maxCol {
			maxCol = len(rows[r])
		}
	}

	for c := 0; c < maxCol; c++ {
		h := combinedHeader(rows, start, end, c)
		if h == "" {
			continue
		}
		switch {
		case cols.forecastMonth == -1 && strings.Contains(h, "прогноз") && strings.Contains(h, "месяц"):
			cols.forecastMonth = c
		case cols.forecast28 == -1 && strings.Contains(h, "прогноз") && strings.Contains(h, "28"):
			cols.forecast28 = c
		case cols.paidQty == -1 && strings.Contains(h, "платных"):
			cols.paidQty = c
		case cols.totalQty == -1 && containsAnyHeader(h, "экземпляр", "кол-во") && !strings.Contains(h, "платн"):
			cols.totalQty = c
		case cols.stock == -1 && strings.Contains(h, "остаток на складах") && strings.Contains(h, "шт"):
			cols.stock = c
		case cols.freeStock == -1 && strings.Contains(h, "размещение без платы") && strings.Contains(h, "шт"):
			cols.freeStock = c
		case cols.forcedUnits == -1 && strings.Contains(h, "вынужденное размещение") && strings.Contains(h, "шт"):
			cols.forcedUnits = c
		case cols.cost == -1 && containsAnyHeader(h, "списан", "стоимост", "начислен", "плата"):
			cols.cost = c
		case cols.date == -1 && isPlacementDateHeader(h):
			cols.date = c
		case cols.offer == -1 && containsAnyHeader(h, "артикул", "offer_id", "offer id", "offer"):
			cols.offer = c
		case cols.sku == -1 && containsAnyHeader(h, "sku", "product_id", "product id"):
			cols.sku = c
		case cols.status == -1 && strings.Contains(h, "статус"):
			cols.status = c
		}
	}
	return cols
}

// isPlacementDateHeader отличает дату окончания бесплатного размещения от
// «Дней до начала…» и прочих числовых колонок.
func isPlacementDateHeader(h string) bool {
	if strings.Contains(h, "дата") && containsAnyHeader(h, "бесплат", "окончан", "начал", "срок") {
		return true
	}
	return containsAnyHeader(h, "бесплатно до", "окончание бесплатного", "срок бесплатного")
}

func parseTableRows(rows [][]string) (map[string]PlacementItemData, error) {
	headerStart, headerEnd := findPlacementHeader(rows)
	if headerStart == -1 {
		return nil, fmt.Errorf("не удалось найти строку заголовков с артикулом/SKU в отчете о стоимости размещения")
	}
	cols := detectPlacementColumns(rows, headerStart, headerEnd)
	if cols.offer == -1 && cols.sku == -1 {
		return nil, fmt.Errorf("в отчете о стоимости размещения не найдены ни артикул, ни SKU")
	}

	type productAccumulator struct {
		OfferID           string
		SKU               string
		PlacementInfo     string
		PastStorageCost   float64
		FreeUntil         time.Time
		TotalPaidQty      float64
		TotalQty          float64
		OzonForecast28    float64
		OzonForecastMonth float64
		HasOzonForecast   bool
		Stock             int
		FreeStock         int
		ForcedUnits       int
	}

	accMap := make(map[string]*productAccumulator)

	for i := headerEnd + 1; i < len(rows); i++ {
		row := rows[i]
		offerID := cellAt(row, cols.offer)
		sku := cellAt(row, cols.sku)
		if offerID == "" && sku == "" {
			continue
		}
		statusVal := cellAt(row, cols.status)
		dateVal := cellAt(row, cols.date)

		key := offerID
		if key == "" {
			key = sku
		}

		acc, ok := accMap[key]
		if !ok {
			acc = &productAccumulator{
				OfferID: offerID,
				SKU:     sku,
			}
			accMap[key] = acc
		}

		acc.PastStorageCost += parseCost(cellAt(row, cols.cost))
		acc.TotalPaidQty += parseCost(cellAt(row, cols.paidQty))
		acc.TotalQty += parseCost(cellAt(row, cols.totalQty))
		acc.Stock += int(parseCost(cellAt(row, cols.stock)))
		acc.FreeStock += int(parseCost(cellAt(row, cols.freeStock)))
		acc.ForcedUnits += int(parseCost(cellAt(row, cols.forcedUnits)))

		// Прогноз Ozon есть только в выгрузке личного кабинета, в файле API
		// таких колонок нет — тогда прогноз считает программа.
		if cols.forecast28 != -1 {
			acc.OzonForecast28 += parseCost(cellAt(row, cols.forecast28))
			acc.HasOzonForecast = true
		}
		if cols.forecastMonth != -1 {
			acc.OzonForecastMonth += parseCost(cellAt(row, cols.forecastMonth))
			acc.HasOzonForecast = true
		}

		if freeUntil := parsePlacementDate(dateVal); !freeUntil.IsZero() {
			acc.FreeUntil = freeUntil
		}

		if dateVal != "" || statusVal != "" {
			acc.PlacementInfo = formatPlacementInfo(statusVal, dateVal)
		}
	}

	result := make(map[string]PlacementItemData, len(accMap)*2)
	for _, acc := range accMap {
		info := acc.PlacementInfo
		switch {
		case cols.paidQty != -1 || cols.totalQty != -1:
			if acc.TotalPaidQty > 0 || acc.PastStorageCost > 0 {
				info = "Платно"
			} else if acc.TotalQty > 0 {
				info = "Бесплатно"
			} else {
				info = "-"
			}
		case cols.stock != -1:
			// В выгрузке ЛК готового статуса нет — выводим его из данных Ozon:
			// вынужденное размещение и прогноз говорят о платном хранении.
			switch {
			case acc.Stock == 0:
				info = "-"
			case acc.ForcedUnits > 0 || acc.OzonForecast28 > 0:
				info = "Платно"
			case !acc.FreeUntil.IsZero():
				info = fmt.Sprintf("Бесплатно (до %s)", acc.FreeUntil.Format("02.01.2006"))
			default:
				info = "Бесплатно"
			}
		case info == "":
			if acc.PastStorageCost > 0 {
				info = "Платно"
			} else {
				info = "-"
			}
		}

		item := PlacementItemData{
			OfferID:           acc.OfferID,
			SKU:               acc.SKU,
			PlacementInfo:     info,
			PastStorageCost:   acc.PastStorageCost,
			FreeUntil:         acc.FreeUntil,
			OzonForecast28:    acc.OzonForecast28,
			OzonForecastMonth: acc.OzonForecastMonth,
			HasOzonForecast:   acc.HasOzonForecast,
		}

		if acc.OfferID != "" {
			result[acc.OfferID] = item
		}
		if acc.SKU != "" {
			result[acc.SKU] = item
		}
	}

	return result, nil
}

func parseCost(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "₽", "")
	s = strings.ReplaceAll(s, "руб.", "")
	s = strings.ReplaceAll(s, "руб", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

// placementDateLayouts — форматы даты в отчете Ozon (со временем или без него).
var placementDateLayouts = []string{"2006-01-02", "02.01.2006"}

// parsePlacementDate разбирает дату окончания бесплатного размещения. Нулевое
// время означает «даты нет» — пустая ячейка, прочерк или null.
func parsePlacementDate(raw string) time.Time {
	d := strings.TrimSpace(raw)
	if d == "" || d == "-" || d == "—" || strings.EqualFold(d, "null") {
		return time.Time{}
	}
	if idx := strings.IndexAny(d, "T "); idx != -1 {
		d = d[:idx]
	}
	for _, layout := range placementDateLayouts {
		if parsed, err := time.Parse(layout, d); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func formatPlacementInfo(statusVal, dateVal string) string {
	s := strings.ToLower(strings.TrimSpace(statusVal))
	d := strings.TrimSpace(dateVal)

	if parsedDate := parsePlacementDate(d); !parsedDate.IsZero() {
		if time.Now().After(parsedDate) {
			return "Платно"
		}
		return fmt.Sprintf("Бесплатно (до %s)", parsedDate.Format("02.01.2006"))
	}

	if d != "" && d != "-" && d != "—" && !strings.EqualFold(d, "null") {
		// Дата есть, но формат незнакомый — показываем как пришло.
		return fmt.Sprintf("Бесплатно (до %s)", d)
	}

	// Важно: "бесплатно" содержит подстроку "платно", поэтому сначала проверяем "бесплатн"!
	if strings.Contains(s, "бесплатн") || s == "да" || s == "true" || s == "free" {
		return "Бесплатно"
	}
	if strings.Contains(s, "платн") || s == "нет" || s == "false" || s == "paid" {
		return "Платно"
	}
	if s == "" {
		return "-"
	}
	return statusVal
}
