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
	sheetName   = "Поставки"
	legendSheet = "Легенда"

	titleRow     = 1
	groupRow     = 2
	headerRow    = 3
	firstDataRow = 4

	// Колонки до блоков кластеров: артикул, остаток FBS, размещение, прошлые
	// списания и прогноз. Прогноз бывает двух видов: собственный прогноз Ozon
	// из выгрузки ЛК (две колонки) или оценка программы (одна колонка).
	baseColumnsEstimate = 5
	baseColumnsOzon     = 6

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
	storageFreeUntil
	storageNone // товара нет на складах FBO
	storageUnknown
)

var storageTints = map[storageKind]cellTint{
	storagePaid:      {fill: "FFC7CE", font: "9C0006"}, // платно — красный
	storageFree:      {fill: "C6EFCE", font: "006100"}, // бесплатно — зелёный
	storageFreeUntil: {fill: "FFEB9C", font: "9C6500"}, // бесплатно до даты — жёлтый
	storageNone:      {fill: "EDEDED", font: "808080"}, // размещения нет — серый
	storageUnknown:   {fill: colorWhite, font: "000000"},
}

var statusTints = map[string]cellTint{
	domain.StatusOutOfStock: {fill: "FFC7CE", font: "9C0006"},
	domain.StatusNeedsFBO:   {fill: "FCE4D6", font: "833C0C"},
	domain.StatusNeedsFBS:   {fill: "FFF2CC", font: "7F6000"},
	domain.StatusOK:         {fill: "C6EFCE", font: "006100"},
}

// classifyStorage определяет статус платного хранения по тексту из отчёта Ozon.
// Проверка «бесплатн» обязана идти первой: в слове «бесплатно» есть подстрока «платно».
func classifyStorage(placement string) storageKind {
	text := strings.ToLower(strings.TrimSpace(placement))
	switch {
	case text == "" || text == "-" || strings.Contains(text, "нет"):
		return storageNone
	case strings.Contains(text, "бесплатн"):
		if strings.Contains(text, "до") {
			return storageFreeUntil
		}
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
// тогда причина попадает в титул и на лист «Легенда».
func ExportToExcel(rows []domain.ProductSupplyRow, selectedClusters []string, placementNote string) (string, error) {
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

	if err := writeHeaders(file, styles, sheet, selectedClusters, columnCount, baseCount, lastColumn, titleText(rows, placementNote)); err != nil {
		return "", err
	}

	specs := buildColumnSpecs(selectedClusters, columnCount, baseCount)
	if err := writeDataRows(file, styles, sheet, rows, selectedClusters, specs, baseCount); err != nil {
		return "", err
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

func titleText(rows []domain.ProductSupplyRow, placementNote string) string {
	paid, outOfStock := 0, 0
	spent, forecast, forecastMonth, soldOutSpent := 0.0, 0.0, 0.0, 0.0
	hasOzon := false
	for _, row := range rows {
		if classifyStorage(row.PlacementInfo) == storagePaid {
			paid++
		}
		if row.OverallStatus == domain.StatusOutOfStock {
			outOfStock++
		}
		spent += row.StorageCost
		if row.TotalFBOStock == 0 {
			soldOutSpent += row.StorageCost
		}
		if row.HasOzonStorageForecast {
			hasOzon = true
		}
		forecast += row.OzonStorageForecast28
		forecastMonth += row.OzonStorageForecastMonth
	}

	spentPart := fmt.Sprintf("списано за хранение: %s ₽", formatRub(spent))
	if soldOutSpent > 0 {
		// Без этой оговорки сумма расходится с личным кабинетом: там позиции,
		// снятые с FBO, в список размещения не попадают.
		spentPart += fmt.Sprintf(" (из них без остатка на FBO: %s ₽)", formatRub(soldOutSpent))
	}

	forecastPart := fmt.Sprintf("оценка хранения за %d дн.: %s ₽", domain.DefaultStorageForecastDays, formatRub(estimateSum(rows)))
	if hasOzon {
		forecastPart = fmt.Sprintf("прогноз Ozon: %s ₽ на 28 дн., %s ₽ до конца месяца", formatRub(forecast), formatRub(forecastMonth))
	}

	return fmt.Sprintf(
		"Сводка поставок Ozon · %s · позиций: %d · платное хранение: %d · %s · %s · закончилось: %d",
		time.Now().Format("02.01.2006 15:04"), len(rows), paid, spentPart, forecastPart, outOfStock,
	) + placementSuffix(placementNote)
}

// placementSuffix дописывает в титул предупреждение, если отчет о размещении
// не загрузился: иначе пустые колонки хранения читаются как «ничего не списали».
func placementSuffix(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return " · ВНИМАНИЕ: " + note
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
	for _, cluster := range clusters {
		headers = append(headers,
			fmt.Sprintf("Продажи %s за период (шт)", cluster),
			fmt.Sprintf("Остаток FBO %s", cluster),
			fmt.Sprintf("Поставка FBO %s", cluster),
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
	lastColumn string,
	title string,
) error {
	if err := file.SetCellValue(sheet, "A1", title); err != nil {
		return fmt.Errorf("не удалось записать заголовок Excel: %w", err)
	}
	if err := file.MergeCell(sheet, "A1", lastColumn+"1"); err != nil {
		return fmt.Errorf("не удалось объединить заголовок Excel: %w", err)
	}
	titleSpec := cellSpec{fill: colorTitle, font: colorWhite, align: "left", size: 14, bold: true}
	if err := applyStyle(file, styles, sheet, "A1", lastColumn+"1", titleSpec); err != nil {
		return err
	}

	// Полоса групп: общий блок колонок и отдельный блок на каждый кластер.
	if err := file.SetCellValue(sheet, "A2", "Товар · остаток · хранение"); err != nil {
		return fmt.Errorf("не удалось записать заголовок группы Excel: %w", err)
	}
	baseLast, err := excelize.ColumnNumberToName(baseCount)
	if err != nil {
		return fmt.Errorf("не удалось определить колонку Excel: %w", err)
	}
	if err := file.MergeCell(sheet, "A2", baseLast+"2"); err != nil {
		return fmt.Errorf("не удалось объединить заголовок группы Excel: %w", err)
	}
	baseBand := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "left", bold: true}
	if err := applyStyle(file, styles, sheet, "A2", baseLast+"2", baseBand); err != nil {
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
		band := cellSpec{fill: paletteFor(i).header, font: colorWhite, align: "center", bold: true}
		if err := applyStyle(file, styles, sheet, from, to, band); err != nil {
			return err
		}
	}

	headers := buildHeaders(clusters, baseCount)
	headerValues := make([]interface{}, len(headers))
	for i, header := range headers {
		headerValues[i] = header
	}
	if err := file.SetSheetRow(sheet, "A3", &headerValues); err != nil {
		return fmt.Errorf("не удалось записать заголовки Excel: %w", err)
	}

	for col := 1; col <= columnCount; col++ {
		spec := cellSpec{fill: colorBaseHeader, font: colorWhite, align: "center", bold: true}
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
	widths := map[string]float64{"A": 30, "B": 12, "C": 26, "D": 22, "E": 24}
	if baseCount == baseColumnsOzon {
		widths["F"] = 24
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
		if err := file.SetColWidth(sheetName, start, end, 15); err != nil {
			return fmt.Errorf("не удалось настроить ширину столбцов Excel: %w", err)
		}
	}

	if err := file.SetColWidth(sheetName, lastColumn, lastColumn, 16); err != nil {
		return fmt.Errorf("не удалось настроить ширину столбца Excel: %w", err)
	}

	// Титул в две строки: в нем и сумма прошлых списаний, и прогноз, поэтому
	// высота 30 обрезала бы текст на узких отчетах.
	for row, height := range map[int]float64{titleRow: 44, groupRow: 22, headerRow: 46} {
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

	if err := writeRow("КЛАСТЕРЫ — ЦВЕТ БЛОКА КОЛОНОК", "Продажи за период · Остаток FBO · Поставка FBO", section); err != nil {
		return err
	}
	for i, cluster := range clusters {
		palette := paletteFor(i)
		label := cellSpec{fill: palette.header, font: colorWhite, align: "left", bold: true}
		if err := writeRow(cluster, "Продажи за период · Остаток FBO · Поставка FBO (акцентный цвет)", label); err != nil {
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
		{"Бесплатно (до ДД.ММ.ГГГГ)", "Бесплатное размещение с датой окончания — после неё хранение станет платным", storageFreeUntil},
		{"Бесплатно", "Бесплатное размещение без даты окончания", storageFree},
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
