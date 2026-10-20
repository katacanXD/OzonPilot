package printer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"CLIForOZON/internal/domain"
)

const (
	sheetName    = "Поставки"
	summarySheet = "Общая информация"
	legendSheet  = "Легенда"

	// Титул со сводкой живет на отдельном листе «Общая информация», поэтому
	// на «Поставках» сверху сразу идут полоса групп и заголовки: их закрепляем,
	// а данные начинаются с третьей строки.
	groupRow     = 1
	headerRow    = 2
	firstDataRow = 3

	// Колонки до блоков кластеров: артикул, остаток FBS, размещение, прошлые
	// списания и прогноз. Прогноз бывает двух видов: собственный прогноз Ozon
	// из выгрузки ЛК (две колонки) или оценка программы (одна колонка).
	baseColumnsEstimate = 5
	baseColumnsOzon     = 6

	// Заголовки колонок узкие и многострочные, поэтому шрифт мельче данных:
	// иначе строка заголовков занимает пол-экрана.
	headerFontSize = 9.0
	dataFontSize   = 10.0

	colorTitle      = "1F3864" // тёмно-синий: титул отчета
	colorBaseHeader = "2F5597" // синий: заголовки общих колонок
	colorWhite      = "FFFFFF"
	colorGrid       = "B4C6E7" // тонкая сетка
	colorGridBold   = "1F3864" // разделитель блоков кластеров
)

// clusterPalette задает цветовую гамму блока колонок одного кластера.
type clusterPalette struct {
	header string // фон заголовков блока
	body   string // фон ячеек данных
	accent string // фон колонки «Поставка»
}

// clusterPalettes чередуются по кластерам, поэтому соседние блоки колонок
// всегда разного цвета.
var clusterPalettes = []clusterPalette{
	{header: "1F4E79", body: "DDEBF7", accent: "BDD7EE"}, // синий
	{header: "375623", body: "E2EFDA", accent: "C6E0B4"}, // зелёный
	{header: "833C0C", body: "FCE4D6", accent: "F8CBAD"}, // оранжевый
	{header: "5B2C6F", body: "E4DFEC", accent: "D1C4E9"}, // фиолетовый
	{header: "1F6F6B", body: "DAF0EE", accent: "B7E1DE"}, // бирюзовый
	{header: "7B2D26", body: "F8D7DA", accent: "F1B0B7"}, // терракотовый
	{header: "7F6000", body: "FFF2CC", accent: "FFE699"}, // золотой
	{header: "1F3864", body: "D9E2F3", accent: "B4C6E7"}, // индиго
}

func paletteFor(index int) clusterPalette {
	return clusterPalettes[index%len(clusterPalettes)]
}

// cellTint — пара «заливка + цвет шрифта» для смысловой подсветки ячейки.
type cellTint struct {
	fill string
	font string
}

// storageKind — статус платного хранения товара на FBO.
type storageKind int

const (
	storagePaid storageKind = iota
	storageFree
	storageNone // товара нет на складах FBO
	storageUnknown
)

var storageTints = map[storageKind]cellTint{
	storagePaid:    {fill: "FFC7CE", font: "9C0006"}, // платно — красный
	storageFree:    {fill: "C6EFCE", font: "006100"}, // бесплатно — зелёный
	storageNone:    {fill: "EDEDED", font: "808080"}, // размещения нет — серый
	storageUnknown: {fill: colorWhite, font: "000000"},
}

var statusTints = map[string]cellTint{
	domain.StatusOutOfStock: {fill: "FFC7CE", font: "9C0006"},
	domain.StatusNeedsFBO:   {fill: "FCE4D6", font: "833C0C"},
	domain.StatusNeedsFBS:   {fill: "FFF2CC", font: "7F6000"},
	domain.StatusOK:         {fill: "C6EFCE", font: "006100"},
}

// classifyStorage определяет статус платного хранения по тексту из отчёта Ozon.
// Проверка «бесплатн» обязана идти первой: в слове «бесплатно» есть подстрока «платно».
//
// Любое бесплатное размещение — зелёное. Дата окончания бесплатного периода
// остаётся в тексте ячейки («Бесплатно (до ДД.ММ.ГГГГ)»), но цвет не меняет:
// жёлтый читался как «скоро платно» и путал, хотя платного тарифа ещё нет.
func classifyStorage(placement string) storageKind {
	text := strings.ToLower(strings.TrimSpace(placement))
	switch {
	case text == "" || text == "-" || strings.Contains(text, "нет"):
		return storageNone
	case strings.Contains(text, "бесплатн"):
		return storageFree
	case strings.Contains(text, "платн"):
		return storagePaid
	}
	return storageUnknown
}

// cellSpec описывает оформление ячейки. Стили кэшируются по этому ключу:
// excelize.NewStyle на каждую ячейку заметно замедляет экспорт больших отчетов.
type cellSpec struct {
	fill, font string
	align      string
	customFmt  string
	size       float64
	numFmt     int
	bold       bool
	blockStart bool // утолщённая левая граница — начало блока колонок кластера
}

type styleCache struct {
	file  *excelize.File
	cache map[cellSpec]int
}

func (c *styleCache) id(spec cellSpec) (int, error) {
	if cached, ok := c.cache[spec]; ok {
		return cached, nil
	}

	left := excelize.Border{Type: "left", Style: 1, Color: colorGrid}
	if spec.blockStart {
		left = excelize.Border{Type: "left", Style: 2, Color: colorGridBold}
	}
	size := spec.size
	if size == 0 {
		size = 11
	}
	style := &excelize.Style{
		Font: &excelize.Font{Bold: spec.bold, Color: spec.font, Size: size},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{spec.fill}},
		Border: []excelize.Border{
			left,
			{Type: "right", Style: 1, Color: colorGrid},
			{Type: "top", Style: 1, Color: colorGrid},
			{Type: "bottom", Style: 1, Color: colorGrid},
		},
		Alignment: &excelize.Alignment{Horizontal: spec.align, Vertical: "center", WrapText: true},
		NumFmt:    spec.numFmt,
	}
	if spec.customFmt != "" {
		style.CustomNumFmt = &spec.customFmt
	}

	styleID, err := c.file.NewStyle(style)
	if err != nil {
		return 0, err
	}
	c.cache[spec] = styleID
	return styleID, nil
}

// ExportToExcel сохраняет результаты анализа в Excel-таблицу с готовыми фильтрами.
// placementNote — непустая строка, если данные о размещении не загрузились:
// тогда причина попадает на лист «Общая информация» и в «Легенду».
// windowDays — окно продаж: попадает в сводку, чтобы цифры отчета можно было
// сверить с кабинетом.
func ExportToExcel(rows []domain.ProductSupplyRow, selectedClusters []string, placementNote string, windowDays int) (string, error) {
	if len(rows) == 0 {
		return "", fmt.Errorf("нет данных для экспорта")
	}

	sortRows(rows)

	filename, err := reportFilename()
	if err != nil {
		return "", err
	}

	file := excelize.NewFile()
	defer func() {
		_ = file.Close()
	}()

	sheet := sheetName
	if err := file.SetSheetName("Sheet1", sheet); err != nil {
		return "", fmt.Errorf("не удалось переименовать лист Excel: %w", err)
	}

	baseCount := baseColumnCount(rows)
	columnCount := baseCount + len(selectedClusters)*3 + 1
	lastColumn, err := excelize.ColumnNumberToName(columnCount)
	if err != nil {
		return "", fmt.Errorf("не удалось определить диапазон Excel: %w", err)
	}

	styles := &styleCache{file: file, cache: make(map[cellSpec]int)}

	if err := writeHeaders(file, styles, sheet, selectedClusters, columnCount, baseCount); err != nil {
		return "", err
	}

	specs := buildColumnSpecs(selectedClusters, columnCount, baseCount)
	if err := writeDataRows(file, styles, sheet, rows, selectedClusters, specs, baseCount); err != nil {
		return "", err
	}

	// Закрепляем полосу групп и заголовки: данные начинаются с третьей строки.
	if err := file.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		XSplit:      0,
		YSplit:      headerRow,
		TopLeftCell: "A3",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return "", fmt.Errorf("не удалось закрепить верхние строки Excel: %w", err)
	}

	showRowStripes := false
	if err := file.AddTable(sheet, &excelize.Table{
		Range:          fmt.Sprintf("A%d:%s%d", headerRow, lastColumn, len(rows)+headerRow),
		Name:           "SupplyTable",
		StyleName:      "TableStyleLight1", // заливку задаем сами, полосы таблицы не нужны
		ShowRowStripes: &showRowStripes,
	}); err != nil {
		return "", fmt.Errorf("не удалось включить фильтры Excel: %w", err)
	}

	if err := setLayout(file, selectedClusters, lastColumn, baseCount); err != nil {
		return "", err
	}

	// Без вписывания по ширине печать и экспорт в PDF режут колонки кластеров.
	landscape, fitToPage := "landscape", true
	fitToWidth, a4 := 1, 9
	if err := file.SetPageLayout(sheet, &excelize.PageLayoutOptions{
		Orientation: &landscape,
		Size:        &a4,
		FitToWidth:  &fitToWidth,
	}); err != nil {
		return "", fmt.Errorf("не удалось настроить параметры печати Excel: %w", err)
	}
	if err := file.SetSheetProps(sheet, &excelize.SheetPropsOptions{FitToPage: &fitToPage}); err != nil {
		return "", fmt.Errorf("не удалось включить вписывание при печати Excel: %w", err)
	}

	if err := writeSummarySheet(file, styles, rows, selectedClusters, windowDays, placementNote); err != nil {
		return "", err
	}

	if err := writeLegend(file, styles, selectedClusters, baseCount == baseColumnsOzon, placementNote); err != nil {
		return "", err
	}
	file.SetActiveSheet(0)

	if err := file.SaveAs(filename); err != nil {
		return "", fmt.Errorf("не удалось сохранить Excel-файл: %w", err)
	}
	return filename, nil
}

func reportFilename() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("не удалось определить папку программы: %w", err)
	}

	reportDir := filepath.Join(filepath.Dir(executablePath), "reports")
	if err := os.MkdirAll(reportDir, 0755); err != nil {
		return "", fmt.Errorf("не удалось создать папку отчетов: %w", err)
	}

	return filepath.Join(
		reportDir,
		fmt.Sprintf("ozon_supply_%s.xlsx", time.Now().Format("2006-01-02_15-04-05")),
	), nil
}

// baseColumnCount выбирает набор общих колонок: 6 — с прогнозом Ozon из
// выгрузки ЛК (две колонки), 5 — с собственной оценкой хранения.
func baseColumnCount(rows []domain.ProductSupplyRow) int {
	for _, row := range rows {
		if row.HasOzonStorageForecast {
			return baseColumnsOzon
		}
	}
	return baseColumnsEstimate
}

// reportSummary — цифры сводки. Раньше они стояли одной длинной строкой над
// таблицей; теперь это лист «Общая информация».
type reportSummary struct {
	paid, outOfStock        int
	spent, soldOutSpent     float64
	forecast, forecastMonth float64
	hasOzonForecast         bool
}

func summarize(rows []domain.ProductSupplyRow) reportSummary {
	var s reportSummary
	for _, row := range rows {
		if classifyStorage(row.PlacementInfo) == storagePaid {
			s.paid++
		}
		if row.OverallStatus == domain.StatusOutOfStock {
			s.outOfStock++
		}
		s.spent += row.StorageCost
		if row.TotalFBOStock == 0 {
			s.soldOutSpent += row.StorageCost
		}
		if row.HasOzonStorageForecast {
			s.hasOzonForecast = true
		}
		s.forecast += row.OzonStorageForecast28
		s.forecastMonth += row.OzonStorageForecastMonth
	}
	return s
}

// writeSummarySheet создает лист «Общая информация»: сводка в виде таблицы
// «Параметр — Значение» понятнее длинной строки и не занимает место над данными.
func writeSummarySheet(
	file *excelize.File,
	styles *styleCache,
	rows []domain.ProductSupplyRow,
	clusters []string,
	windowDays int,
	placementNote string,
) error {
	if _, err := file.NewSheet(summarySheet); err != nil {
		return fmt.Errorf("не удалось создать лист «Общая информация»: %w", err)
	}

	titleSpec := cellSpec{fill: colorTitle, font: colorWhite, align: "left", size: 14, bold: true}
	if err := file.SetCellValue(summarySheet, "A1", "Сводка отчёта о поставках Ozon"); err != nil {
		return fmt.Errorf("не удалось записать заголовок сводки: %w", err)
	}
	if err := file.MergeCell(summarySheet, "A1", "B1"); err != nil {
		return fmt.Errorf("не удалось объединить заголовок сводки: %w", err)
	}
	if err := applyStyle(file, styles, summarySheet, "A1", "B1", titleSpec); err != nil {
		return err
	}
	if err := file.SetRowHeight(summarySheet, 1, 26); err != nil {
		return fmt.Errorf("не удалось настроить высоту строки сводки: %w", err)
	}

	if err := file.SetColWidth(summarySheet, "A", "A", 42); err != nil {
		return fmt.Errorf("не удалось настроить ширину сводки: %w", err)
	}
	if err := file.SetColWidth(summarySheet, "B", "B", 72); err != nil {
		return fmt.Errorf("не удалось настроить ширину сводки: %w", err)
	}

	headerSpec := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "left", bold: true}
	if err := file.SetCellValue(summarySheet, "A3", "Параметр"); err != nil {
		return fmt.Errorf("не удалось записать заголовок сводки: %w", err)
	}
	if err := file.SetCellValue(summarySheet, "B3", "Значение"); err != nil {
		return fmt.Errorf("не удалось записать заголовок сводки: %w", err)
	}
	if err := applyStyle(file, styles, summarySheet, "A3", "B3", headerSpec); err != nil {
		return err
	}

	s := summarize(rows)
	items := []struct{ label, value string }{
		{"Дата и время отчёта", time.Now().Format("02.01.2006 15:04")},
		{"Позиций в отчёте", fmt.Sprintf("%d", len(rows))},
		{"Кластеры в отчёте", strings.Join(clusters, ", ")},
		{"Окно продаж, дней", fmt.Sprintf("%d", windowDays)},
		{"Платное хранение, шт", fmt.Sprintf("%d", s.paid)},
		{"Списано за хранение, ₽", formatRub(s.spent)},
	}
	if s.soldOutSpent > 0 {
		// Без этой оговорки сумма расходится с личным кабинетом: там позиции,
		// снятые с FBO, в список размещения не попадают.
		items = append(items, struct{ label, value string }{
			"из них без остатка на FBO, ₽",
			formatRub(s.soldOutSpent),
		})
	}
	if s.hasOzonForecast {
		items = append(items,
			struct{ label, value string }{"Прогноз Ozon на 28 дн., ₽", formatRub(s.forecast)},
			struct{ label, value string }{"Прогноз Ozon до конца месяца, ₽", formatRub(s.forecastMonth)},
		)
	} else {
		items = append(items, struct{ label, value string }{
			fmt.Sprintf("Оценка хранения за %d дн., ₽", domain.DefaultStorageForecastDays),
			formatRub(estimateSum(rows)),
		})
	}
	items = append(items, struct{ label, value string }{
		"Закончилось (OUT_OF_STOCK), шт",
		fmt.Sprintf("%d", s.outOfStock),
	})
	if strings.TrimSpace(placementNote) == "" {
		items = append(items, struct{ label, value string }{"Данные о размещении", "получены из Ozon"})
	} else {
		items = append(items, struct{ label, value string }{"Данные о размещении", "ВНИМАНИЕ: " + placementNote})
	}

	labelSpec := cellSpec{fill: colorWhite, font: "000000", align: "left", bold: true}
	valueSpec := cellSpec{fill: colorWhite, font: "000000", align: "left"}
	for i, item := range items {
		row := 4 + i
		labelCell := fmt.Sprintf("A%d", row)
		valueCell := fmt.Sprintf("B%d", row)
		if err := file.SetCellValue(summarySheet, labelCell, item.label); err != nil {
			return fmt.Errorf("не удалось записать строку сводки: %w", err)
		}
		if err := file.SetCellValue(summarySheet, valueCell, item.value); err != nil {
			return fmt.Errorf("не удалось записать строку сводки: %w", err)
		}
		if err := applyStyle(file, styles, summarySheet, labelCell, labelCell, labelSpec); err != nil {
			return err
		}
		if err := applyStyle(file, styles, summarySheet, valueCell, valueCell, valueSpec); err != nil {
			return err
		}
	}
	return nil
}

// estimateSum суммирует собственную оценку хранения: она нужна в титуле,
// только когда прогноза Ozon в отчете нет.
func estimateSum(rows []domain.ProductSupplyRow) float64 {
	total := 0.0
	for _, row := range rows {
		total += row.StorageForecast
	}
	return total
}

// formatRub выводит сумму без копеек, с пробелами между разрядами: в титуле
// отчета крупные суммы без разделителей читаются плохо.
func formatRub(v float64) string {
	digits := fmt.Sprintf("%.0f", v)
	negative := strings.HasPrefix(digits, "-")
	if negative {
		digits = digits[1:]
	}
	var grouped strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteRune(' ')
		}
		grouped.WriteRune(r)
	}
	if negative {
		return "-" + grouped.String()
	}
	return grouped.String()
}

func buildHeaders(clusters []string, baseCount int) []string {
	headers := []string{
		"Артикул (OfferID)",
		"Остаток FBS",
		"Размещение FBO",
		"Списано за хранение (₽)",
	}
	if baseCount == baseColumnsOzon {
		headers = append(headers,
			"Прогноз Ozon на 28 дн. (₽)",
			"Прогноз Ozon до конца месяца (₽)",
		)
	} else {
		headers = append(headers, fmt.Sprintf("Оценка хранения за %d дн. (₽)", domain.DefaultStorageForecastDays))
	}
	for range clusters {
		headers = append(headers,
			// Кластер уже подписан в полосе групп выше, поэтому в заголовках
			// колонок его имя не повторяется — так шапка читается компактнее.
			"Продажи за период FBS и FBO",
			"Остаток FBO",
			"Поставка FBO",
		)
	}
	return append(headers, "Статус")
}

func writeHeaders(
	file *excelize.File,
	styles *styleCache,
	sheet string,
	clusters []string,
	columnCount int,
	baseCount int,
) error {
	// Полоса групп: общий блок колонок и отдельный блок на каждый кластер.
	bandCell, err := excelize.CoordinatesToCellName(1, groupRow)
	if err != nil {
		return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
	}
	if err := file.SetCellValue(sheet, bandCell, "Товар · остаток · хранение"); err != nil {
		return fmt.Errorf("не удалось записать заголовок группы Excel: %w", err)
	}
	bandTo, err := excelize.CoordinatesToCellName(baseCount, groupRow)
	if err != nil {
		return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
	}
	if err := file.MergeCell(sheet, bandCell, bandTo); err != nil {
		return fmt.Errorf("не удалось объединить заголовок группы Excel: %w", err)
	}
	baseBand := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "left", bold: true, size: headerFontSize}
	if err := applyStyle(file, styles, sheet, bandCell, bandTo, baseBand); err != nil {
		return err
	}

	statusCell, err := excelize.CoordinatesToCellName(columnCount, groupRow)
	if err != nil {
		return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
	}
	if err := file.SetCellValue(sheet, statusCell, "Оценка"); err != nil {
		return fmt.Errorf("не удалось записать заголовок группы Excel: %w", err)
	}
	statusBand := baseBand
	statusBand.fill = colorTitle
	statusBand.align = "center"
	if err := applyStyle(file, styles, sheet, statusCell, statusCell, statusBand); err != nil {
		return err
	}

	for i, cluster := range clusters {
		from, err := excelize.CoordinatesToCellName(baseCount+1+i*3, groupRow)
		if err != nil {
			return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
		}
		to, err := excelize.CoordinatesToCellName(baseCount+3+i*3, groupRow)
		if err != nil {
			return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
		}
		if err := file.SetCellValue(sheet, from, cluster); err != nil {
			return fmt.Errorf("не удалось записать кластер Excel: %w", err)
		}
		if err := file.MergeCell(sheet, from, to); err != nil {
			return fmt.Errorf("не удалось объединить заголовок кластера Excel: %w", err)
		}
		band := cellSpec{fill: paletteFor(i).header, font: colorWhite, align: "center", bold: true, size: headerFontSize}
		if err := applyStyle(file, styles, sheet, from, to, band); err != nil {
			return err
		}
	}

	headers := buildHeaders(clusters, baseCount)
	headerValues := make([]interface{}, len(headers))
	for i, header := range headers {
		headerValues[i] = header
	}
	headerStart, err := excelize.CoordinatesToCellName(1, headerRow)
	if err != nil {
		return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
	}
	if err := file.SetSheetRow(sheet, headerStart, &headerValues); err != nil {
		return fmt.Errorf("не удалось записать заголовки Excel: %w", err)
	}

	for col := 1; col <= columnCount; col++ {
		spec := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "center", bold: true, size: headerFontSize}
		switch {
		case col == columnCount:
			spec.fill = colorTitle
		case col > baseCount:
			spec.fill = paletteFor((col - baseCount - 1) / 3).header
			spec.blockStart = (col-baseCount-1)%3 == 0
		}
		cell, err := excelize.CoordinatesToCellName(col, headerRow)
		if err != nil {
			return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
		}
		if err := applyStyle(file, styles, sheet, cell, cell, spec); err != nil {
			return err
		}
	}
	return nil
}

// buildColumnSpecs задает оформление колонок данных; индексация с единицы, как в Excel.
func buildColumnSpecs(clusters []string, columnCount, baseCount int) []cellSpec {
	specs := make([]cellSpec, columnCount+1)

	specs[1] = cellSpec{fill: colorWhite, font: "000000", align: "left", bold: true}
	specs[2] = cellSpec{fill: colorWhite, font: "000000", align: "center"}
	specs[3] = cellSpec{fill: colorWhite, font: "000000", align: "left"}
	// Прошлые списания и прогноз — деньги: одинаковый денежный формат. Колонка
	// прогноза есть всегда: одна (оценка) или две (прогноз Ozon). Незаполненный
	// спек дал бы стиль с пустым цветом, а такой файл Excel/офисные библиотеки
	// воспринимают как битый.
	moneySpec := cellSpec{fill: colorWhite, font: "000000", align: "right", numFmt: 4} // #,##0.00
	specs[4] = moneySpec
	specs[5] = moneySpec
	if baseCount == baseColumnsOzon {
		specs[6] = moneySpec
	}

	for i := range clusters {
		palette := paletteFor(i)
		start := baseCount + 1 + i*3
		// Продажи считаются в штуках, поэтому формат целый: «10», а не «10,0».
		specs[start] = cellSpec{fill: palette.body, font: palette.header, align: "center", customFmt: "#,##0", blockStart: true}
		specs[start+1] = cellSpec{fill: palette.body, font: "000000", align: "center"}
		specs[start+2] = cellSpec{fill: palette.accent, font: palette.header, align: "center", bold: true}
	}

	specs[columnCount] = cellSpec{fill: colorWhite, font: "000000", align: "center", bold: true}

	// Данные мельче заголовков: колонки кластеров узкие, а строк много.
	for i := 1; i <= columnCount; i++ {
		if specs[i].size == 0 {
			specs[i].size = dataFontSize
		}
	}
	return specs
}

func writeDataRows(
	file *excelize.File,
	styles *styleCache,
	sheet string,
	rows []domain.ProductSupplyRow,
	clusters []string,
	specs []cellSpec,
	baseCount int,
) error {
	statusColumn := len(specs) - 1
	rowSpecs := make([]cellSpec, len(specs))

	for rowIndex, row := range rows {
		excelRow := firstDataRow + rowIndex

		placement := row.PlacementInfo
		if placement == "" {
			placement = "-"
		}
		values := []interface{}{row.OfferID, row.CurrentFBS, placement, row.StorageCost}
		if baseCount == baseColumnsOzon {
			values = append(values, row.OzonStorageForecast28, row.OzonStorageForecastMonth)
		} else {
			values = append(values, row.StorageForecast)
		}
		for _, cluster := range clusters {
			clusterData := row.ClusterData[cluster]
			values = append(values, clusterData.PeriodSales, clusterData.StockFBO, clusterData.Supply)
		}
		values = append(values, row.OverallStatus)

		cell, err := excelize.JoinCellName("A", excelRow)
		if err != nil {
			return fmt.Errorf("не удалось определить строку Excel: %w", err)
		}
		if err := file.SetSheetRow(sheet, cell, &values); err != nil {
			return fmt.Errorf("не удалось записать строку Excel: %w", err)
		}

		copy(rowSpecs, specs)
		storage := classifyStorage(row.PlacementInfo)
		rowSpecs[3] = tint(specs[3], storageTints[storage], storage == storagePaid)
		// Прошлые списания и прогноз выделяются красным, только когда есть сумма.
		if row.StorageCost > 0 {
			rowSpecs[4] = tint(specs[4], cellTint{fill: "FFC7CE", font: "9C0006"}, true)
		}
		if baseCount == baseColumnsOzon {
			if row.OzonStorageForecast28 > 0 {
				rowSpecs[5] = tint(specs[5], cellTint{fill: "FFC7CE", font: "9C0006"}, true)
			}
			if row.OzonStorageForecastMonth > 0 {
				rowSpecs[6] = tint(specs[6], cellTint{fill: "FFC7CE", font: "9C0006"}, true)
			}
		} else if row.StorageForecast > 0 {
			rowSpecs[5] = tint(specs[5], cellTint{fill: "FFC7CE", font: "9C0006"}, true)
		}
		rowSpecs[statusColumn] = tint(specs[statusColumn], statusTint(row.OverallStatus), true)

		for col := 1; col < len(rowSpecs); col++ {
			cellName, err := excelize.CoordinatesToCellName(col, excelRow)
			if err != nil {
				return fmt.Errorf("не удалось определить ячейку Excel: %w", err)
			}
			if err := applyStyle(file, styles, sheet, cellName, cellName, rowSpecs[col]); err != nil {
				return err
			}
		}
	}
	return nil
}

func tint(base cellSpec, colors cellTint, bold bool) cellSpec {
	base.fill = colors.fill
	base.font = colors.font
	base.bold = bold
	return base
}

func statusTint(status string) cellTint {
	if colors, ok := statusTints[status]; ok {
		return colors
	}
	return cellTint{fill: "EDEDED", font: "808080"}
}

func setLayout(file *excelize.File, clusters []string, lastColumn string, baseCount int) error {
	// Узкими (8) сделаны все колонки с числами, кроме артикула (A), размещения
	// (C) и статуса: там длинный текст, и им нужно место.
	const narrowWidth = 8.0
	widths := map[string]float64{"A": 24, "B": narrowWidth, "C": 20, "D": narrowWidth, "E": narrowWidth}
	if baseCount == baseColumnsOzon {
		widths["F"] = narrowWidth
	}
	for column, width := range widths {
		if err := file.SetColWidth(sheetName, column, column, width); err != nil {
			return fmt.Errorf("не удалось настроить ширину столбца Excel: %w", err)
		}
	}

	for i := range clusters {
		start, err := excelize.ColumnNumberToName(baseCount + 1 + i*3)
		if err != nil {
			return fmt.Errorf("не удалось определить колонку Excel: %w", err)
		}
		end, err := excelize.ColumnNumberToName(baseCount + 3 + i*3)
		if err != nil {
			return fmt.Errorf("не удалось определить колонку Excel: %w", err)
		}
		if err := file.SetColWidth(sheetName, start, end, narrowWidth); err != nil {
			return fmt.Errorf("не удалось настроить ширину столбцов Excel: %w", err)
		}
	}

	if err := file.SetColWidth(sheetName, lastColumn, lastColumn, 13); err != nil {
		return fmt.Errorf("не удалось настроить ширину столбца Excel: %w", err)
	}

	// В узких колонках (8) заголовки переносятся на 3–4 строки, поэтому высота
	// подобрана так, чтобы текст не обрезался.
	for row, height := range map[int]float64{groupRow: 18, headerRow: 44} {
		if err := file.SetRowHeight(sheetName, row, height); err != nil {
			return fmt.Errorf("не удалось настроить высоту строки Excel: %w", err)
		}
	}
	return nil
}

func applyStyle(file *excelize.File, styles *styleCache, sheet, from, to string, spec cellSpec) error {
	styleID, err := styles.id(spec)
	if err != nil {
		return fmt.Errorf("не удалось создать стиль Excel: %w", err)
	}
	if err := file.SetCellStyle(sheet, from, to, styleID); err != nil {
		return fmt.Errorf("не удалось оформить ячейки Excel: %w", err)
	}
	return nil
}

// writeLegend добавляет лист-расшифровку: без него смысл цветов неочевиден.
func writeLegend(file *excelize.File, styles *styleCache, clusters []string, hasOzonForecast bool, placementNote string) error {
	if _, err := file.NewSheet(legendSheet); err != nil {
		return fmt.Errorf("не удалось создать лист легенды: %w", err)
	}

	if err := file.SetCellValue(legendSheet, "A1", "Легенда отчета о поставках Ozon"); err != nil {
		return fmt.Errorf("не удалось записать заголовок легенды: %w", err)
	}
	if err := file.MergeCell(legendSheet, "A1", "B1"); err != nil {
		return fmt.Errorf("не удалось объединить заголовок легенды: %w", err)
	}
	titleSpec := cellSpec{fill: colorTitle, font: colorWhite, align: "left", size: 14, bold: true}
	if err := applyStyle(file, styles, legendSheet, "A1", "B1", titleSpec); err != nil {
		return err
	}
	if err := file.SetRowHeight(legendSheet, 1, 26); err != nil {
		return fmt.Errorf("не удалось настроить высоту строки легенды: %w", err)
	}

	section := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "left", bold: true}
	plain := cellSpec{fill: colorWhite, font: "000000", align: "left"}

	row := 3
	writeRow := func(label, desc string, labelSpec cellSpec) error {
		labelCell := fmt.Sprintf("A%d", row)
		descCell := fmt.Sprintf("B%d", row)
		if err := file.SetCellValue(legendSheet, labelCell, label); err != nil {
			return fmt.Errorf("не удалось записать строку легенды: %w", err)
		}
		if err := file.SetCellValue(legendSheet, descCell, desc); err != nil {
			return fmt.Errorf("не удалось записать строку легенды: %w", err)
		}
		if err := applyStyle(file, styles, legendSheet, labelCell, labelCell, labelSpec); err != nil {
			return err
		}
		if err := applyStyle(file, styles, legendSheet, descCell, descCell, plain); err != nil {
			return err
		}
		row++
		return nil
	}

	if err := writeRow(
		"КЛАСТЕРЫ — ЦВЕТ БЛОКА КОЛОНОК",
		"Продажи за период FBS и FBO · Остаток FBO · Поставка FBO. Продажи считаются по кластеру назначения (куда уехал товар) и включают FBO + FBS",
		section,
	); err != nil {
		return err
	}
	for i, cluster := range clusters {
		palette := paletteFor(i)
		label := cellSpec{fill: palette.header, font: colorWhite, align: "left", bold: true}
		if err := writeRow(cluster, "Продажи за период FBS и FBO · Остаток FBO · Поставка FBO (акцентный цвет)", label); err != nil {
			return err
		}
	}

	row++
	if err := writeRow("ПЛАТНОЕ ХРАНЕНИЕ FBO", "Подсветка ячейки в колонке «Размещение FBO»", section); err != nil {
		return err
	}
	for _, item := range []struct {
		label string
		desc  string
		kind  storageKind
	}{
		{"Платно", "Тариф платный: за остаток на FBO начисляется хранение (см. две денежные колонки)", storagePaid},
		{"Бесплатно", "Бесплатное размещение; в скобках — дата, когда бесплатный период закончится", storageFree},
		{"-", "Товара нет на складах FBO: размещения нет, будущих списаний не будет", storageNone},
	} {
		if err := writeRow(item.label, item.desc, tint(plain, storageTints[item.kind], true)); err != nil {
			return err
		}
	}

	row++
	if err := writeRow(
		"ХРАНЕНИЕ В ДЕНЬГАХ (₽)",
		"Прошлые списания и прогноз: что уже списали и что спишут",
		section,
	); err != nil {
		return err
	}

	var moneyRows []struct{ label, desc string }
	if hasOzonForecast {
		moneyRows = []struct{ label, desc string }{
			{"Списано за хранение (₽)", "прошлое списание Ozon за последние 28 дней — то же, что в личном кабинете"},
			{"Прогноз Ozon на 28 дн. (₽)", "колонка «Прогноз стоимости размещения на 28 дней» из выгрузки ЛК"},
			{"Прогноз Ozon до конца месяца (₽)", "колонка «Прогноз стоимости размещения до конца месяца» из выгрузки ЛК"},
			{"как считает Ozon", "объём товара в литрах × тариф ₽/л/день × дни платного размещения; после бесплатного периода (120/150/365 дней с даты приемки) — 2,5 ₽/л/день"},
			{"что такое «без остатка на FBO»", "в титуле: сумма прошлых списаний по позициям, которые уже распроданы; в личном кабинете такие позиции в список не попадают"},
		}
	} else {
		moneyRows = []struct{ label, desc string }{
			{"Списано за хранение (₽)", "прошлое списание Ozon за последние 28 дней"},
			{fmt.Sprintf("Оценка хранения за %d дн. (₽)", domain.DefaultStorageForecastDays), "оценка программы, а не прогноз Ozon: точный прогноз есть только в выгрузке ЛК «Стоимость размещения по товарам»"},
			{"тариф", "₽ за экземпляр в день = прошлое списание из отчета Ozon ÷ (дни отчетного периода × средний остаток за период)"},
			{"средний остаток", "текущий остаток FBO по всем кластерам + продажи за период ÷ 2"},
			{"распродажа", "по каждому дню горизонта остаток уменьшается на среднесуточные продажи, поэтому быстро продаваемый товар дает меньшую оценку"},
			{"бесплатный период", "дни до даты «Бесплатно (до …)» не тарифицируются"},
			{"0 ₽ в оценке", "нет остатка на FBO, размещение бесплатное или тариф неизвестен (не было прошлых списаний)"},
		}
	}
	for _, item := range moneyRows {
		if err := writeRow(item.label, item.desc, plain); err != nil {
			return err
		}
	}
	if err := writeRow("красная сумма", "списание или прогноз больше нуля — деньги за хранение уже или еще будут", tint(plain, cellTint{fill: "FFC7CE", font: "9C0006"}, true)); err != nil {
		return err
	}

	if placementNote != "" {
		row++
		if err := writeRow("ДАННЫЕ О РАЗМЕЩЕНИИ НЕ ЗАГРУЖЕНЫ", placementNote, tint(plain, cellTint{fill: "FFC7CE", font: "9C0006"}, true)); err != nil {
			return err
		}
		if err := writeRow("-", "поэтому «Размещение FBO», «Списано за хранение» и прогноз пустые; полный текст ошибки — в консоли", plain); err != nil {
			return err
		}
	}

	row++
	if err := writeRow("СТАТУС ПОСТАВКИ", "Подсветка последней колонки отчета", section); err != nil {
		return err
	}
	for _, item := range []struct {
		status string
		desc   string
	}{
		{domain.StatusOutOfStock, "Закончился и на FBO, и на FBS при активном спросе — упущенная прибыль"},
		{domain.StatusNeedsFBO, "Дефицит на складах Ozon — нужна поставка"},
		{domain.StatusNeedsFBS, "Пусто на собственном складе продавца (FBS)"},
		{domain.StatusOK, "Запаса достаточно"},
	} {
		if err := writeRow(item.status, item.desc, tint(plain, statusTint(item.status), true)); err != nil {
			return err
		}
	}

	for column, width := range map[string]float64{"A": 32, "B": 78} {
		if err := file.SetColWidth(legendSheet, column, column, width); err != nil {
			return fmt.Errorf("не удалось настроить ширину столбца легенды: %w", err)
		}
	}
	return nil
}

func sortRows(rows []domain.ProductSupplyRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		if rows[i].TotalSupply != rows[j].TotalSupply {
			return rows[i].TotalSupply > rows[j].TotalSupply
		}
		if rows[i].TotalSales != rows[j].TotalSales {
			return rows[i].TotalSales > rows[j].TotalSales
		}
		return rows[i].OfferID < rows[j].OfferID
	})
}
