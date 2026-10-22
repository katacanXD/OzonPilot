package printer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"CLIForOZON/internal/domain"
)

func TestExportToExcelColumns(t *testing.T) {
	rows := []domain.ProductSupplyRow{
		{
			OfferID:         "TEST-1",
			Name:            "Товар Тестовый",
			CurrentFBS:      10,
			TotalSupply:     50,
			OverallStatus:   domain.StatusOK,
			Priority:        4,
			PlacementInfo:   "Бесплатно (до 15.11.2026)",
			StorageForecast: 125.50,
			TotalFBOStock:   20,
			ClusterData: map[string]domain.ClusterStockData{
				"Москва": {
					PeriodSales: 20,
					DailySales:  0.7,
					StockFBO:    15,
					Supply:      5,
				},
				"Санкт-Петербург": {
					PeriodSales: 10,
					DailySales:  0.35,
					StockFBO:    5,
					Supply:      0,
				},
			},
		},
	}

	clusters := []string{"Москва", "Санкт-Петербург"}
	filePath, err := ExportToExcel(rows, clusters, "", 28)
	if err != nil {
		t.Fatalf("ExportToExcel failed: %v", err)
	}
	defer func() {
		_ = os.Remove(filePath)
		// Clean up reports dir if empty
		_ = os.Remove(filepath.Dir(filePath))
	}()

	f, err := excelize.OpenFile(filePath)
	if err != nil {
		t.Fatalf("failed to open generated excel: %v", err)
	}
	defer func() { _ = f.Close() }()

	allRows, err := f.GetRows(sheetName)
	if err != nil || len(allRows) < headerRow {
		t.Fatalf("failed to read rows: %v", err)
	}

	// Заголовки колонок лежат во второй строке: выше только полоса групп.
	headerRowValues := allRows[headerRow-1]
	// Check that "Наименование" and "Итого поставка" are not in headerRow
	for _, h := range headerRowValues {
		if h == "Наименование" {
			t.Errorf("колонка 'Наименование' не должна присутствовать в заголовках: %v", headerRowValues)
		}
		if h == "Итого поставка (шт)" {
			t.Errorf("колонка 'Итого поставка (шт)' не должна присутствовать в заголовках: %v", headerRowValues)
		}
	}

	// В отчете обе денежные колонки: прошлые списания и оценка хранения.
	foundPlacement := false
	foundSpent := false
	foundForecast := false
	wantForecastHeader := fmt.Sprintf("Оценка за %d дн. (₽)", domain.DefaultStorageForecastDays)
	for _, h := range headerRowValues {
		switch h {
		case "Размещение FBO":
			foundPlacement = true
		case "Списано за хранение (₽)":
			foundSpent = true
		case wantForecastHeader:
			foundForecast = true
		}
	}

	if !foundPlacement {
		t.Errorf("колонка 'Размещение FBO' не найдена в заголовках: %v", headerRowValues)
	}
	if !foundSpent {
		t.Errorf("колонка 'Списано за хранение (₽)' не найдена в заголовках: %v", headerRowValues)
	}
	if !foundForecast {
		t.Errorf("колонка %q не найдена в заголовках: %v", wantForecastHeader, headerRowValues)
	}

	// Первые две колонки полосы групп пустые: подпись «Хранение» стоит над
	// размещением и деньгами.
	if got := allRows[groupRow-1][2]; got != "Хранение" {
		t.Errorf("полоса групп должна начинаться с «Хранение», получено %q", got)
	}

	// Продажи считаются в штуках: формат целый, без «20,0».
	salesStyleID, err := f.GetCellStyle(sheetName, "G3")
	if err != nil {
		t.Fatalf("GetCellStyle(G3): %v", err)
	}
	salesStyle, err := f.GetStyle(salesStyleID)
	if err != nil {
		t.Fatalf("GetStyle(G3): %v", err)
	}
	if salesStyle.CustomNumFmt == nil || *salesStyle.CustomNumFmt != "#,##0" {
		t.Errorf("формат колонки продаж = %v, ожидался целый #,##0", salesStyle.CustomNumFmt)
	}
}

func TestClassifyStorage(t *testing.T) {
	cases := map[string]storageKind{
		// «бесплатно» содержит подстроку «платно», поэтому важен порядок проверок
		"Платно":                    storagePaid,
		"Бесплатно":                 storageFree,
		"Бесплатно (до 15.11.2026)": storageFree, // дата в тексте, цвет всё равно зелёный
		"бесплатно (до 01.12.2026)": storageFree,
		"":                          storageNone,
		"-":                         storageNone,
		"Нет на FBO":                storageNone,
	}

	for placement, want := range cases {
		if got := classifyStorage(placement); got != want {
			t.Errorf("classifyStorage(%q) = %d, want %d", placement, got, want)
		}
	}
}

func TestExportToExcelColors(t *testing.T) {
	clusterData := map[string]domain.ClusterStockData{
		"Москва":          {PeriodSales: 86, StockFBO: 0, Supply: 71},
		"Санкт-Петербург": {PeriodSales: 5, StockFBO: 2, Supply: 3},
	}
	rows := []domain.ProductSupplyRow{
		{
			OfferID:         "PAID",
			CurrentFBS:      0,
			OverallStatus:   domain.StatusOutOfStock,
			Priority:        1,
			PlacementInfo:   "Платно",
			StorageCost:     1429.00,
			StorageForecast: 1450.50,
			TotalFBOStock:   12,
			ClusterData:     clusterData,
		},
		{
			OfferID:       "FREE",
			CurrentFBS:    5,
			OverallStatus: domain.StatusOK,
			Priority:      4,
			PlacementInfo: "Бесплатно (до 15.11.2026)",
			ClusterData:   clusterData,
		},
		{
			// Товар распродан: на складах Ozon ничего нет, прогноз хранения 0.
			OfferID:       "SOLD-OUT",
			OverallStatus: domain.StatusOutOfStock,
			Priority:      1,
			ClusterData:   clusterData,
		},
	}

	clusters := []string{"Москва", "Санкт-Петербург"}
	filePath, err := ExportToExcel(rows, clusters, "", 28)
	if err != nil {
		t.Fatalf("ExportToExcel failed: %v", err)
	}
	defer func() {
		_ = os.Remove(filePath)
		_ = os.Remove(filepath.Dir(filePath))
	}()

	f, err := excelize.OpenFile(filePath)
	if err != nil {
		t.Fatalf("failed to open generated excel: %v", err)
	}
	defer func() { _ = f.Close() }()

	// У каждого кластера своя гамма: заголовок блока и его ячейки данных.
	moscow := cellFill(t, f, "G3")
	petersburg := cellFill(t, f, "J3")
	if moscow != strings.ToUpper(paletteFor(0).body) || petersburg != strings.ToUpper(paletteFor(1).body) {
		t.Errorf("ячейки кластеров окрашены не по своей гамме: Москва=%q, СПб=%q", moscow, petersburg)
	}
	if moscow == petersburg {
		t.Errorf("блоки кластеров должны быть разного цвета, получен %q", moscow)
	}
	if got := cellFill(t, f, "G2"); got != strings.ToUpper(paletteFor(0).header) {
		t.Errorf("заголовок блока Москва окрашен в %q, ожидался %q", got, paletteFor(0).header)
	}
	if got := cellFill(t, f, "I3"); got != strings.ToUpper(clusterPalettes[0].accent) {
		t.Errorf("колонка «Поставка FBO» окрашена в %q, ожидался акцент %q", got, clusterPalettes[0].accent)
	}

	// Порядок: PAID, SOLD-OUT (оба приоритет 1, артикул PAID раньше), затем FREE.
	paid := cellFill(t, f, "C3")
	soldOut := cellFill(t, f, "C4")
	free := cellFill(t, f, "C5")
	if paid == "" || paid == free {
		t.Errorf("статусы платного и бесплатного хранения должны различаться: платно=%q, бесплатно=%q", paid, free)
	}
	if soldOut != strings.ToUpper(storageTints[storageNone].fill) {
		t.Errorf("у распроданного товара размещение должно быть серым: получено %q", soldOut)
	}
	// Прошлые списания и прогноз — две отдельные колонки, обе подсвечены.
	if got := cellFill(t, f, "D3"); got != strings.ToUpper("FFC7CE") {
		t.Errorf("прошлое списание должно быть подсвечено: получено %q", got)
	}
	if got := cellFill(t, f, "E3"); got == "" || got == strings.ToUpper("FF") {
		t.Errorf("прогноз хранения должен быть подсвечен: получено %q", got)
	}
	// Нулевая оценка не должна получать незаполненный стиль: Excel рисует такие
	// ячейки ярко-синим, а офисные библиотеки считают файл битым.
	if got := cellFill(t, f, "E5"); got != strings.ToUpper(colorWhite) {
		t.Errorf("ячейка с нулевой оценкой должна быть белой, получено %q", got)
	}
	if got := cellFill(t, f, "E4"); got == strings.ToUpper("FFC7CE") {
		t.Error("нулевой прогноз хранения не должен выделяться красным")
	}

	if _, err := f.GetSheetIndex(legendSheet); err != nil {
		t.Errorf("лист легенды недоступен: %v", err)
	}
}

func TestExportToExcelOzonForecastColumns(t *testing.T) {
	rows := []domain.ProductSupplyRow{
		{
			OfferID:                  "OZON-1",
			CurrentFBS:               2,
			OverallStatus:            domain.StatusOK,
			Priority:                 4,
			PlacementInfo:            "Платно",
			StorageCost:              4593,
			OzonStorageForecast28:    1946.69,
			OzonStorageForecastMonth: 3744.75,
			HasOzonStorageForecast:   true,
			TotalFBOStock:            40,
			ClusterData: map[string]domain.ClusterStockData{
				"Москва": {PeriodSales: 9, StockFBO: 40},
			},
		},
	}

	filePath, err := ExportToExcel(rows, []string{"Москва"}, "", 28)
	if err != nil {
		t.Fatalf("ExportToExcel failed: %v", err)
	}
	defer func() {
		_ = os.Remove(filePath)
		_ = os.Remove(filepath.Dir(filePath))
	}()

	f, err := excelize.OpenFile(filePath)
	if err != nil {
		t.Fatalf("failed to open generated excel: %v", err)
	}
	defer func() { _ = f.Close() }()

	allRows, err := f.GetRows(sheetName)
	if err != nil || len(allRows) < firstDataRow {
		t.Fatalf("failed to read rows: %v", err)
	}
	header := allRows[headerRow-1]
	if len(header) < 11 {
		t.Fatalf("ожидалось 11 колонок, получено %d: %v", len(header), header)
	}
	// Колонки Ozon идут после прошлых списаний, блоки кластеров сдвинуты.
	if header[3] != "Списано за хранение (₽)" || header[4] != "Прогноз Ozon на 28 дн. (₽)" || header[5] != "Прогноз Ozon до конца месяца (₽)" {
		t.Errorf("колонки прогноза Ozon не на своих местах: %v", header)
	}
	if header[6] != "Продажи всего за 28 дней (шт)" || header[7] != "Продажи за 28 дней FBS и FBO" {
		t.Errorf("колонка «Продажи всего» и блок кластера не на своих местах: %v", header)
	}
	if header[10] != "Статус" {
		t.Errorf("последняя колонка должна быть статусом: %v", header)
	}
	// Обе колонки прогноза — деньги, поэтому изменяемые суммы подсвечены.
	for _, cell := range []string{"E3", "F3"} {
		if got := cellFill(t, f, cell); got == "" {
			t.Errorf("прогноз в %s должен быть подсвечен", cell)
		}
	}
}

func TestExportToExcelPlacementWarning(t *testing.T) {
	rows := []domain.ProductSupplyRow{{
		OfferID:       "X",
		OverallStatus: domain.StatusOK,
		Priority:      4,
		ClusterData:   map[string]domain.ClusterStockData{"Москва": {PeriodSales: 1, StockFBO: 3}},
	}}
	note := "данные о размещении Ozon не получены (таймаут отчета)"

	filePath, err := ExportToExcel(rows, []string{"Москва"}, note, 28)
	if err != nil {
		t.Fatalf("ExportToExcel failed: %v", err)
	}
	defer func() {
		_ = os.Remove(filePath)
		_ = os.Remove(filepath.Dir(filePath))
	}()

	f, err := excelize.OpenFile(filePath)
	if err != nil {
		t.Fatalf("failed to open generated excel: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Титул со сводкой теперь на отдельном листе «Общая информация».
	summary, err := f.GetRows(summarySheet)
	if err != nil {
		t.Fatalf("failed to read summary sheet: %v", err)
	}
	foundNote := false
	for _, row := range summary {
		for _, cell := range row {
			if strings.Contains(cell, "ВНИМАНИЕ") && strings.Contains(cell, note) {
				foundNote = true
			}
		}
	}
	if !foundNote {
		t.Errorf("на листе «Общая информация» нет предупреждения о размещении")
	}

	legend, err := f.GetRows(legendSheet)
	if err != nil {
		t.Fatalf("failed to read legend: %v", err)
	}
	found := false
	for _, row := range legend {
		for _, cell := range row {
			if strings.Contains(cell, note) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("на листе «Легенда» нет причины отсутствия данных о размещении")
	}
}

func TestExportToExcelLayout(t *testing.T) {
	rows := []domain.ProductSupplyRow{{
		OfferID:       "X",
		OverallStatus: domain.StatusOK,
		Priority:      4,
		ClusterData:   map[string]domain.ClusterStockData{"Москва": {PeriodSales: 1, StockFBO: 3}},
	}}

	filePath, err := ExportToExcel(rows, []string{"Москва"}, "", 28)
	if err != nil {
		t.Fatalf("ExportToExcel failed: %v", err)
	}
	defer func() {
		_ = os.Remove(filePath)
		_ = os.Remove(filepath.Dir(filePath))
	}()

	f, err := excelize.OpenFile(filePath)
	if err != nil {
		t.Fatalf("failed to open generated excel: %v", err)
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	want := []string{sheetName, summarySheet, legendSheet}
	if len(sheets) != len(want) {
		t.Fatalf("листов %d, ожидалось %d: %v", len(sheets), len(want), sheets)
	}
	for i := range want {
		if sheets[i] != want[i] {
			t.Errorf("лист %d = %q, ожидался %q (порядок: %v)", i, sheets[i], want[i], sheets)
		}
	}

	// Закреп: полоса групп, заголовки и колонка с артикулом.
	panes, err := f.GetPanes(sheetName)
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if !panes.Freeze || panes.YSplit != headerRow || panes.XSplit != 1 || panes.TopLeftCell != "B3" {
		t.Errorf("закреп должен стоять на строках 1–2 и колонке A (B3), получено %+v", panes)
	}

	allRows, err := f.GetRows(sheetName)
	if err != nil {
		t.Fatalf("failed to read rows: %v", err)
	}
	// Полоса групп: A и B пустые, «Хранение» над размещением и деньгами,
	// «Продажи» над колонкой всего, дальше названия кластеров.
	if got := allRows[groupRow-1][0]; got != "" {
		t.Errorf("первая колонка полосы групп должна быть пустой, получено %q", got)
	}
	if got := allRows[groupRow-1][1]; got != "" {
		t.Errorf("вторая колонка полосы групп должна быть пустой, получено %q", got)
	}
	if got := allRows[groupRow-1][2]; got != "Хранение" {
		t.Errorf("над размещением и деньгами ожидалась подпись «Хранение», получено %q", got)
	}
	if got := allRows[groupRow-1][5]; got != "Продажи" {
		t.Errorf("над колонкой всего ожидалась подпись «Продажи», получено %q", got)
	}
	if got := allRows[groupRow-1][6]; got != "Москва" {
		t.Errorf("над блоком кластера ожидалось название кластера, получено %q", got)
	}
	if got := allRows[headerRow-1][0]; got != "Артикул (OfferID)" {
		t.Errorf("во второй строке должны быть заголовки, получено %q", got)
	}
	if got := allRows[firstDataRow-1][0]; got != "X" {
		t.Errorf("данные должны начинаться с третьей строки, получено %q", got)
	}

	// Ширины: артикул и размещение шире, «Продажи всего» и продажи кластера —
	// 12.63, остаток и поставка — 7.42, статус — 13.
	statusColumn, err := excelize.ColumnNumberToName(10) // 6 базовых + 3 кластера + статус
	if err != nil {
		t.Fatalf("ColumnNumberToName: %v", err)
	}
	widths := map[string]float64{
		"A": 24, "B": 8, "C": 19.26, "D": 11.84, "E": 11.21,
		"F": 12.63, "G": 12.63, "H": 7.42, "I": 7.42,
		statusColumn: 13,
	}
	for column, want := range widths {
		got, err := f.GetColWidth(sheetName, column)
		if err != nil {
			t.Fatalf("GetColWidth(%s): %v", column, err)
		}
		if got != want {
			t.Errorf("ширина колонки %s = %v, ожидалось %v", column, got, want)
		}
	}

	// Окно продаж попадает в сводку: по нему цифры сверяются с кабинетом.
	summary, err := f.GetRows(summarySheet)
	if err != nil {
		t.Fatalf("failed to read summary: %v", err)
	}
	found := false
	for _, row := range summary {
		for _, cell := range row {
			if strings.Contains(cell, "Окно продаж") {
				found = true
			}
		}
	}
	if !found {
		t.Error("на листе «Общая информация» нет окна продаж")
	}
}

func cellFill(t *testing.T, f *excelize.File, cell string) string {
	t.Helper()

	styleID, err := f.GetCellStyle(sheetName, cell)
	if err != nil {
		t.Fatalf("GetCellStyle(%s): %v", cell, err)
	}
	style, err := f.GetStyle(styleID)
	if err != nil {
		t.Fatalf("GetStyle(%s): %v", cell, err)
	}
	if len(style.Fill.Color) == 0 {
		return ""
	}
	return strings.ToUpper(style.Fill.Color[0])
}
