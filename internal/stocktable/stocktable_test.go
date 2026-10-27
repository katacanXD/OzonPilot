package stocktable

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"CLIForOZON/internal/domain"
)

func TestExportURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"ссылка на лист с gid в query и фрагменте",
			"https://docs.google.com/spreadsheets/d/ABC123/edit?gid=1093032725#gid=1093032725",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=1093032725",
		},
		{
			"gid только во фрагменте",
			"https://docs.google.com/spreadsheets/d/ABC123/edit#gid=42",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=42",
		},
		{
			"без gid — первый лист",
			"https://docs.google.com/spreadsheets/d/ABC123/edit",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv",
		},
		{
			"готовый адрес экспорта не меняется",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=7",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=7",
		},
		{
			"gviz приводится к экспорту",
			"https://docs.google.com/spreadsheets/d/ABC123/gviz/tq?tqx=out:csv&gid=7",
			"https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=7",
		},
		{
			"опубликованная таблица",
			"https://docs.google.com/spreadsheets/d/e/2PACX/pubhtml?gid=5",
			"https://docs.google.com/spreadsheets/d/e/2PACX/pub?output=csv&gid=5",
		},
		{"чужой хост", "https://example.com/spreadsheets/d/ABC/edit", ""},
		{"не Google Docs", "https://docs.google.com/document/d/ABC/edit", ""},
		{"не ссылка", "просто текст", ""},
	}

	for _, tt := range tests {
		got, err := ExportURL(tt.in)
		if tt.want == "" {
			if err == nil {
				t.Errorf("%s: ожидалась ошибка, получено %q", tt.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s:\n получено %q\n ожидалось %q", tt.name, got, tt.want)
		}
	}
}

const testCSV = `"А","Столбец 1","Название","Артикул WB/ozon","Офис","Гараж"
"1","YBR","Амортизаторы задние YBR 125","Амортизаторы YBR","128",""
"2","YBR","Багажник YBR125","","5",""
"3","YBR","Глушитель YBR125","Глушитель YBR125","1 уценка",""
"4","YBR","Ветровик","  Ветровик   кр. фары YBR  ","1 234",""
"5","YBR","Товар без наличия","Нет остатка","",""
`

func TestParseCSV(t *testing.T) {
	table, err := parseCSV(strings.NewReader(testCSV))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}

	tests := []struct {
		offer, name string
		qty         int
		text        string
	}{
		{"Амортизаторы YBR", "", 128, ""},
		{"", "Багажник YBR125", 5, ""},          // запасной вариант по названию
		{"Глушитель YBR125", "", 0, "1 уценка"}, // пояснение вместо числа
		{"ветровик кр. фары ybr", "", 1234, ""}, // регистр и двойные пробелы
		{"Нет остатка", "", 0, ""},              // пустая ячейка — строки нет
	}

	for _, tt := range tests {
		cell, ok := table.Lookup(tt.offer, tt.name)
		if tt.offer == "Нет остатка" {
			if ok {
				t.Errorf("пустая ячейка не должна давать наличие: %+v", cell)
			}
			continue
		}
		if !ok {
			t.Errorf("не найдено наличие для offer=%q name=%q", tt.offer, tt.name)
			continue
		}
		if tt.text != "" {
			if cell.Text != tt.text {
				t.Errorf("текст = %q, ожидалось %q", cell.Text, tt.text)
			}
			continue
		}
		if cell.Qty == nil || *cell.Qty != tt.qty {
			t.Errorf("количество = %v, ожидалось %d", cell.Qty, tt.qty)
		}
	}
}

func TestParseCSVWithoutColumns(t *testing.T) {
	_, err := parseCSV(strings.NewReader(`"Название","Остаток"` + "\n" + `"Товар","5"`))
	if err == nil {
		t.Fatal("ожидалась ошибка: в таблице нет колонок артикула и «Офис»")
	}
}

func TestApply(t *testing.T) {
	table, err := parseCSV(strings.NewReader(testCSV))
	if err != nil {
		t.Fatalf("parseCSV: %v", err)
	}

	rows := []domain.ProductSupplyRow{
		{OfferID: "Амортизаторы YBR"},
		{OfferID: "UNKNOWN", Name: "Багажник YBR125"},
		{OfferID: "UNKNOWN-2", Name: "Нет в таблице"},
	}
	if got := table.Apply(rows); got != 2 {
		t.Errorf("совпадений = %d, ожидалось 2", got)
	}
	if rows[0].ExternalStock == nil || rows[0].ExternalStock.Qty == nil || *rows[0].ExternalStock.Qty != 128 {
		t.Errorf("наличие по артикулу не проставлено: %+v", rows[0].ExternalStock)
	}
	if rows[1].ExternalStock == nil || rows[1].ExternalStock.Qty == nil || *rows[1].ExternalStock.Qty != 5 {
		t.Errorf("наличие по названию не проставлено: %+v", rows[1].ExternalStock)
	}
	if rows[2].ExternalStock != nil {
		t.Errorf("для товара вне таблицы наличие должно быть nil: %+v", rows[2].ExternalStock)
	}
}

func TestFetchPrivateSheet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Sign in</body></html>"))
	}))
	defer srv.Close()

	_, err := fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "не опубликована") {
		t.Fatalf("ожидалась ошибка про закрытый доступ, получено: %v", err)
	}
}

func TestFetchCSV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testCSV))
	}))
	defer srv.Close()

	data, err := fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(data), "Офис") {
		t.Errorf("в ответе нет CSV: %q", string(data))
	}
}
