package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// loadTestConfig создаёт отдельный каталог конфига: тесты не должны трогать
// настоящий config.yaml пользователя.
func loadTestConfig(t *testing.T) (*AppConfig, string) {
	t.Helper()
	viper.Reset()

	appDir := filepath.Join(t.TempDir(), "CLIForOZON")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("не удалось создать каталог конфига: %v", err)
	}
	t.Setenv("APPDATA", filepath.Dir(appDir))

	path := filepath.Join(appDir, "config.yaml")
	if err := os.WriteFile(path, []byte("client_id: \"1\"\napi_key: \"key\"\n"), 0644); err != nil {
		t.Fatalf("не удалось записать конфиг: %v", err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg, path
}

func TestAskStockURLSavesExportLink(t *testing.T) {
	cfg, path := loadTestConfig(t)

	var out strings.Builder
	link := "https://docs.google.com/spreadsheets/d/ABC123/edit?gid=42#gid=42"
	if err := AskStockURL(cfg, strings.NewReader(link+"\n"), &out); err != nil {
		t.Fatalf("AskStockURL: %v", err)
	}

	want := "https://docs.google.com/spreadsheets/d/ABC123/export?format=csv&gid=42"
	if cfg.StockURL != want {
		t.Errorf("в конфиг записан %q, ожидался готовый адрес CSV %q", cfg.StockURL, want)
	}
	if !cfg.StockPrompted {
		t.Error("после ответа вопрос должен быть помечен как заданный")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать конфиг: %v", err)
	}
	if !strings.Contains(string(data), "stock_url:") || !strings.Contains(string(data), "gid=42") {
		t.Errorf("ссылка не сохранена в файл:\n%s", data)
	}
	if !strings.Contains(string(data), "stock_prompted: true") {
		t.Errorf("отметка об ответе не сохранена в файл:\n%s", data)
	}
}

func TestAskStockURLDecline(t *testing.T) {
	cfg, path := loadTestConfig(t)

	var out strings.Builder
	if err := AskStockURL(cfg, strings.NewReader("n\n"), &out); err != nil {
		t.Fatalf("AskStockURL: %v", err)
	}
	if cfg.StockURL != "" {
		t.Errorf("после отказа ссылка должна быть пустой, получено %q", cfg.StockURL)
	}
	if !cfg.StockPrompted {
		t.Error("отказ должен запоминаться, иначе вопрос повторится")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать конфиг: %v", err)
	}
	if !strings.Contains(string(data), "stock_prompted: true") {
		t.Errorf("отказ не сохранён в файл:\n%s", data)
	}
	if strings.Contains(string(data), "stock_url: http") {
		t.Errorf("в конфиге не должно остаться ссылки:\n%s", data)
	}
}

func TestAskStockURLRetriesInvalidLink(t *testing.T) {
	cfg, _ := loadTestConfig(t)

	var out strings.Builder
	input := "не ссылка\nhttps://docs.google.com/spreadsheets/d/ABC/edit#gid=1\n"
	if err := AskStockURL(cfg, strings.NewReader(input), &out); err != nil {
		t.Fatalf("AskStockURL: %v", err)
	}
	if !strings.Contains(cfg.StockURL, "export?format=csv") {
		t.Errorf("после неверного ввода должна сохраниться рабочая ссылка, получено %q", cfg.StockURL)
	}
	if !strings.Contains(out.String(), "Не подходит") {
		t.Errorf("пользователю не объяснили, что ссылка не подходит:\n%s", out.String())
	}
}
