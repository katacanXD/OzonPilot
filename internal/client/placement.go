package client

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// PlacementItemData — строка отчета Ozon о стоимости размещения товаров.
type PlacementItemData struct {
	OfferID       string
	SKU           string
	PlacementInfo string // "Платно", "Бесплатно (до ДД.ММ.ГГГГ)", "Нет на FBO"
	// PastStorageCost — списано за период отчета, ₽. Хранится не для показа:
	// по нему оценивается тариф хранения (₽ за экземпляр в день).
	PastStorageCost float64
	// FreeUntil — дата окончания бесплатного размещения (нулевая, если нет).
	FreeUntil time.Time
	// OzonForecast28 и OzonForecastMonth — собственный прогноз Ozon из выгрузки
	// личного кабинета «Стоимость размещения по товарам»: «на 28 дней» и «до
	// конца месяца». В файле API этих колонок нет.
	OzonForecast28    float64
	OzonForecastMonth float64
	HasOzonForecast   bool
}

// ParsePlacementReportFile считывает отчет о стоимости размещения из локального файла (.xlsx или .csv).
func ParsePlacementReportFile(filePath string) (map[string]PlacementItemData, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения файла отчета: %w", err)
	}
	return ParsePlacementReportData(data, filePath)
}

// ParsePlacementReportData разбирает байты отчета (автоматически определяя формат: Excel или CSV).
func ParsePlacementReportData(data []byte, filenameHint string) (map[string]PlacementItemData, error) {
	// Сначала пробуем разобрать как Excel (.xlsx)
	if f, err := excelize.OpenReader(bytes.NewReader(data)); err == nil {
		defer func() { _ = f.Close() }()
		return parsePlacementExcel(f)
	}

	// Если не Excel, разбираем как CSV
	return parsePlacementCSV(data)
}

func parsePlacementExcel(f *excelize.File) (map[string]PlacementItemData, error) {
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("Excel-файл не содержит листов")
	}

	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения строк Excel-файла: %w", err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("Excel-файл не содержит данных")
	}

	return parseTableRows(rows)
}

func parsePlacementCSV(data []byte) (map[string]PlacementItemData, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	delimiter := ';'
	firstLineBytes := data
	if idx := bytes.IndexByte(data, '\n'); idx != -1 {
		firstLineBytes = data[:idx]
	}
	firstLine := string(firstLineBytes)
	if strings.Count(firstLine, ";") < strings.Count(firstLine, ",") {
		delimiter = ','
	} else if strings.Count(firstLine, ";") < strings.Count(firstLine, "\t") {
		delimiter = '\t'
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delimiter
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("ошибка разбора CSV-файла: %w", err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("CSV-файл не содержит данных")
	}

	return parseTableRows(rows)
}
