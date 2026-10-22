package client

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParsePlacementReportCSV(t *testing.T) {
	csvContent := `Артикул;SKU;Статус размещения;Дата начала платного размещения;Стоимость размещения, руб.
OFFER-PAID;1001;Платно;-;1450,50
OFFER-FREE;1002;Бесплатно;2026-12-01;0
OFFER-NONE;1003;-;-;0.0
`
	items, err := ParsePlacementReportData([]byte(csvContent), "report.csv")
	if err != nil {
		t.Fatalf("ParsePlacementReportData failed: %v", err)
	}

	if item, ok := items["OFFER-PAID"]; !ok {
		t.Errorf("OFFER-PAID не найден")
	} else {
		if item.PlacementInfo != "Платно" {
			t.Errorf("expected 'Платно', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 1450.50 {
			t.Errorf("expected 1450.50, got %f", item.PastStorageCost)
		}
	}

	if item, ok := items["OFFER-FREE"]; !ok {
		t.Errorf("OFFER-FREE не найден")
	} else {
		if item.PlacementInfo != "Бесплатно (до 01.12.2026)" {
			t.Errorf("expected 'Бесплатно (до 01.12.2026)', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 0 {
			t.Errorf("expected 0, got %f", item.PastStorageCost)
		}
		if item.FreeUntil.Format("2006-01-02") != "2026-12-01" {
			t.Errorf("expected дата окончания бесплатного размещения 2026-12-01, got %v", item.FreeUntil)
		}
	}
}

func TestParsePlacementReportExcel(t *testing.T) {
	f := excelize.NewFile()
	sheet := "Лист1"
	f.SetSheetName("Sheet1", sheet)

	_ = f.SetSheetRow(sheet, "A1", &[]interface{}{"Артикул товара", "SKU", "Статус", "Бесплатно до", "Списано за период"})
	_ = f.SetSheetRow(sheet, "A2", &[]interface{}{"OFFER-X", "2001", "Бесплатно", "15.11.2026", "0.00"})
	_ = f.SetSheetRow(sheet, "A3", &[]interface{}{"OFFER-Y", "2002", "Платно", "", "350,25 ₽"})

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("failed to write excel buffer: %v", err)
	}

	items, err := ParsePlacementReportData(buf.Bytes(), "report.xlsx")
	if err != nil {
		t.Fatalf("ParsePlacementReportData Excel failed: %v", err)
	}

	if item, ok := items["OFFER-X"]; !ok {
		t.Errorf("OFFER-X не найден")
	} else {
		if item.PlacementInfo != "Бесплатно (до 15.11.2026)" {
			t.Errorf("expected 'Бесплатно (до 15.11.2026)', got %q", item.PlacementInfo)
		}
	}

	if item, ok := items["OFFER-Y"]; !ok {
		t.Errorf("OFFER-Y не найден")
	} else {
		if item.PlacementInfo != "Платно" {
			t.Errorf("expected 'Платно', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 350.25 {
			t.Errorf("expected 350.25, got %f", item.PastStorageCost)
		}
	}
}

// Выгрузка личного кабинета «Стоимость размещения по товарам» устроена сложнее
// файла API: две строки заголовков (группы и подзаголовки) и колонки прогноза.
func TestParsePlacementReportLKExport(t *testing.T) {
	f := excelize.NewFile()
	sheet := "Страница #1"
	f.SetSheetName("Sheet1", sheet)

	row1 := []interface{}{
		"Название товара", "Категория товара", "Описательный тип", "Артикул", "SKU",
		"Стоимость за последние 28 дней", "",
		"Прогноз стоимости размещения до конца месяца",
		"Прогноз стоимости размещения на 28 дней",
		"Размещение на 18.10.2026",
	}
	row2 := make([]interface{}, 22)
	row2[5] = "Списано денег, руб"
	row2[6] = "Доля расходов от оборота, %"
	row2[9] = "Остаток на складах, шт"
	row2[10] = "Остаток на складах, л"
	row2[11] = "Зарезервировано под вывоз, шт"
	row2[12] = "Транзит, шт"
	row2[13] = "Транзит, л"
	row2[14] = "Размещение без платы, шт"
	row2[15] = "Размещение без платы, л"
	row2[16] = "Прогноз вынужденного размещения"
	row2[17] = "Вынужденное размещение, шт"
	row2[18] = "Вынужденное размещение, л"
	row2[19] = "Дней до начала вынужденного размещения"
	row2[20] = "Дата начала вынужденного размещения"
	row2[21] = "Списано денег, руб"

	paid := make([]interface{}, 22)
	paid[0] = "Фильтр воздушный Bajaj Dominar"
	paid[3] = "Фильтр возд. Dominar"
	paid[4] = "2251836561"
	paid[5] = 4592.59
	paid[6] = -0.4415
	paid[7] = 3744.75
	paid[8] = 1946.69
	paid[9] = 40
	paid[10] = 30.8
	paid[14] = 4
	paid[15] = 3.08
	paid[17] = 36
	paid[18] = 27.72
	paid[21] = 69.32 // стоимость размещения в день, не прошлое списание

	free := make([]interface{}, 22)
	free[0] = "Фара передняя Stels Flame"
	free[3] = "Фара без креп. Flame"
	free[4] = "1730232576"
	free[5] = 1476
	free[7] = 684
	free[8] = 0
	free[9] = 1
	free[10] = 14.4
	free[14] = 1
	free[15] = 14.4
	free[17] = 0
	free[18] = 0
	free[20] = "2027-09-27"
	free[21] = 36

	for cell, values := range map[string][]interface{}{
		"A1": row1, "A2": row2, "A3": paid, "A4": free,
	} {
		if err := f.SetSheetRow(sheet, cell, &values); err != nil {
			t.Fatalf("failed to write %s: %v", cell, err)
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("failed to write excel buffer: %v", err)
	}

	items, err := ParsePlacementReportData(buf.Bytes(), "ozon_lk.xlsx")
	if err != nil {
		t.Fatalf("ParsePlacementReportData LK failed: %v", err)
	}

	if item, ok := items["Фильтр возд. Dominar"]; !ok {
		t.Errorf("Фильтр возд. Dominar не найден")
	} else {
		if item.PastStorageCost != 4592.59 {
			t.Errorf("прошлое списание = %v, ожидалось 4592.59", item.PastStorageCost)
		}
		if !item.HasOzonForecast || item.OzonForecast28 != 1946.69 || item.OzonForecastMonth != 3744.75 {
			t.Errorf("прогноз Ozon разобран неверно: %+v", item)
		}
		if item.PlacementInfo != "Платно" {
			t.Errorf("статус = %q, ожидался 'Платно'", item.PlacementInfo)
		}
	}

	if item, ok := items["Фара без креп. Flame"]; !ok {
		t.Errorf("Фара без креп. Flame не найдена")
	} else {
		if item.OzonForecast28 != 0 || item.OzonForecastMonth != 684 {
			t.Errorf("прогноз Ozon разобран неверно: %+v", item)
		}
		if item.PlacementInfo != "Бесплатно (до 27.09.2027)" {
			t.Errorf("статус = %q, ожидался 'Бесплатно (до 27.09.2027)'", item.PlacementInfo)
		}
	}
}
