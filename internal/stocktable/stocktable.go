// Package stocktable читает наличие из Google-таблицы продавца: колонка
// «Офис» листа, ссылка на который сохранена в config.yaml.
package stocktable

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"CLIForOZON/internal/domain"
)

// maxBody ограничивает ответ Google: таблица на 760 строк занимает ~200 КБ.
const maxBody = 10 << 20

// Заголовки ищутся по имени, а не по индексу: лист могут переставить, и тогда
// лучше ошибка, чем чужие числа в отчёте.
var (
	articleHeaders = []string{"артикул wb/ozon", "артикул ozon", "артикул", "offer_id", "offer id"}
	stockHeader    = "офис"
	nameHeader     = "название"
)

// Table — наличие по артикулам и названиям товаров.
type Table struct {
	byOffer map[string]domain.ExternalStockCell
	byName  map[string]domain.ExternalStockCell
}

// ExportURL приводит ссылку на лист Google к адресу CSV. Уже готовый адрес
// экспорта возвращается тем же — результат сохраняется в конфиг, поэтому
// повторно разбирать ссылку при каждом запуске не нужно.
func ExportURL(link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("это не ссылка на Google-таблицу")
	}
	if u.Host != "docs.google.com" && u.Host != "spreadsheets.google.com" {
		return "", errors.New("поддерживаются только ссылки docs.google.com")
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	gid := linkGID(u)
	switch {
	case len(parts) >= 4 && parts[0] == "spreadsheets" && parts[1] == "d" && parts[2] == "e":
		// Опубликованная таблица: другой идентификатор и другой адрес CSV.
		return joinCSV("https://docs.google.com/spreadsheets/d/e/"+parts[3]+"/pub", "output", gid), nil
	case len(parts) >= 3 && parts[0] == "spreadsheets" && parts[1] == "d":
		return joinCSV("https://docs.google.com/spreadsheets/d/"+parts[2]+"/export", "format", gid), nil
	default:
		return "", errors.New("в ссылке нет идентификатора таблицы")
	}
}

func linkGID(u *url.URL) string {
	if gid := u.Query().Get("gid"); gid != "" {
		return gid
	}
	if q, err := url.ParseQuery(u.Fragment); err == nil {
		return q.Get("gid")
	}
	return ""
}

func joinCSV(base, formatParam, gid string) string {
	if gid == "" {
		return base + "?" + formatParam + "=csv"
	}
	return base + "?" + formatParam + "=csv&gid=" + url.QueryEscape(gid)
}

// Load скачивает таблицу и разбирает её.
func Load(ctx context.Context, link string) (*Table, error) {
	exportURL, err := ExportURL(link)
	if err != nil {
		return nil, err
	}
	data, err := fetch(ctx, exportURL)
	if err != nil {
		return nil, err
	}
	return parseCSV(bytes.NewReader(data))
}

func fetch(ctx context.Context, exportURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exportURL, nil)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать запрос к таблице: %w", err)
	}
	req.Header.Set("User-Agent", "OzonPilot")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось скачать таблицу: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("таблица недоступна: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать таблицу: %w", err)
	}
	if len(data) > maxBody {
		return nil, fmt.Errorf("таблица больше %d МБ", maxBody>>20)
	}
	// Приватный лист отдаёт HTML страницы входа вместо CSV.
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return nil, errors.New("таблица не опубликована: откройте доступ «Все, у кого есть ссылка — Читатель»")
	}
	return data, nil
}

func parseCSV(r io.Reader) (*Table, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("не удалось разобрать CSV таблицы: %w", err)
	}

	headerRow, article, stock, name := -1, -1, -1, -1
	for i := 0; i < len(rows) && i < 5; i++ {
		a, s, n := findColumns(rows[i])
		if a >= 0 && s >= 0 {
			headerRow, article, stock, name = i, a, s, n
			break
		}
	}
	if headerRow == -1 {
		return nil, errors.New(`в таблице не найдены колонки «Артикул WB/ozon» и «Офис»`)
	}

	table := &Table{
		byOffer: make(map[string]domain.ExternalStockCell),
		byName:  make(map[string]domain.ExternalStockCell),
	}
	for _, row := range rows[headerRow+1:] {
		cell, ok := parseCell(cellAt(row, stock))
		if !ok {
			continue
		}
		if key := normalise(cellAt(row, article)); key != "" {
			table.byOffer[key] = cell
		}
		if key := normalise(cellAt(row, name)); key != "" {
			table.byName[key] = cell
		}
	}
	if len(table.byOffer) == 0 && len(table.byName) == 0 {
		return nil, errors.New("в таблице нет строк с наличием")
	}
	return table, nil
}

func findColumns(header []string) (article, stock, name int) {
	article, stock, name = -1, -1, -1
	for i, cell := range header {
		h := normalise(cell)
		switch {
		case article < 0 && contains(articleHeaders, h):
			article = i
		case stock < 0 && h == stockHeader:
			stock = i
		case name < 0 && h == nameHeader:
			name = i
		}
	}
	return article, stock, name
}

// parseCell разбирает ячейку: пустая — данных нет; целое — число; иначе текст
// («1 уценка», «3 битых»), который тоже показываем.
func parseCell(raw string) (domain.ExternalStockCell, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return domain.ExternalStockCell{}, false
	}
	if n, err := strconv.Atoi(strings.NewReplacer(" ", "", "\u00a0", "").Replace(s)); err == nil {
		return domain.ExternalStockCell{Qty: &n}, true
	}
	return domain.ExternalStockCell{Text: s}, true
}

// Lookup ищет наличие по артикулу, затем по названию товара.
func (t *Table) Lookup(offerID, name string) (domain.ExternalStockCell, bool) {
	if cell, ok := t.byOffer[normalise(offerID)]; ok {
		return cell, true
	}
	cell, ok := t.byName[normalise(name)]
	return cell, ok
}

// Apply проставляет наличие строкам отчёта и возвращает число совпадений.
func (t *Table) Apply(rows []domain.ProductSupplyRow) int {
	matched := 0
	for i := range rows {
		if cell, ok := t.Lookup(rows[i].OfferID, rows[i].Name); ok {
			rows[i].ExternalStock = &cell
			matched++
		}
	}
	return matched
}

// normalise убирает различия, из-за которых артикулы не совпадут: регистр,
// хвостовые пробелы и двойные пробелы внутри (в листе они встречаются).
func normalise(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func cellAt(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
