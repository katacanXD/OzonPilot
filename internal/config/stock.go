package config

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"CLIForOZON/internal/stocktable"
)

const maxStockAttempts = 3

// AskStockURL спрашивает ссылку на таблицу наличия и сохраняет ответ.
// Отказ (n) запоминается, чтобы больше не спрашивать; вернуть вопрос можно
// флагом -ask-stock. Сохраняется готовый адрес CSV, а не исходная ссылка.
func AskStockURL(cfg *AppConfig, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, `Добавить в отчёт столбец «Наличие "Офис"» из Google-таблицы?`)
	fmt.Fprintln(out, "Вставьте ссылку на лист или введите n, чтобы больше не спрашивать:")
	if cfg.StockURL != "" {
		fmt.Fprintf(out, "Сейчас: %s\n", cfg.StockURL)
	}

	for attempt := 1; attempt <= maxStockAttempts; attempt++ {
		fmt.Fprint(out, "> ")
		line, err := readLine(reader)
		if err != nil {
			return fmt.Errorf("не удалось прочитать ответ: %w", err)
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "n", "no", "нет":
			cfg.StockURL = ""
			cfg.StockPrompted = true
			saveStockDecision(cfg, out)
			return nil
		case "":
			fmt.Fprintln(out, "Пустой ответ: вставьте ссылку или введите n.")
			continue
		}

		exportURL, err := stocktable.ExportURL(line)
		if err != nil {
			fmt.Fprintf(out, "Не подходит: %v\n", err)
			continue
		}
		cfg.StockURL = exportURL
		cfg.StockPrompted = true
		saveStockDecision(cfg, out)
		return nil
	}

	// Битый ввод не должен блокировать отчёт: считаем это отказом.
	cfg.StockURL = ""
	cfg.StockPrompted = true
	saveStockDecision(cfg, out)
	return nil
}

func saveStockDecision(cfg *AppConfig, out io.Writer) {
	if err := SaveConfig(cfg); err != nil {
		slog.Warn("Не удалось сохранить настройку наличия", "err", err)
		fmt.Fprintln(out, "Не удалось сохранить настройку — она подействует только на этот запуск.")
		return
	}
	fmt.Fprintln(out, "Настройка сохранена в config.yaml.")
}
