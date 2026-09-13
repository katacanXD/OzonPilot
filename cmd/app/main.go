package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strings"

	"CLIForOZON/internal/client"
	"CLIForOZON/internal/config"
	"CLIForOZON/internal/domain"
	"CLIForOZON/internal/printer"
)

func main() {
	filePath := flag.String("file", "", "Путь к мок-файлу JSON (offline режим)")
	clustersFlag := flag.String("clusters", "Москва", "Целевые кластеры через запятую (например: Москва)")
	windowDaysFlag := flag.Int("window", 14, "Окно расчета продаж (в днях)")
	targetDaysFlag := flag.Int("target-days", 20, "На сколько дней обеспечить запас")
	useFBSFlag := flag.Bool("use-fbs", false, "Учитывать ли FBS остаток как доступный при расчете дефицита FBO")
	resetKeys := flag.Bool("reset-keys", false, "Сбросить сохраненные API ключи Ozon")
	debugFlag := flag.Bool("debug", false, "Включить подробный вывод отладки")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *debugFlag {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	rawClusters := strings.Split(*clustersFlag, ",")
	selectedClusters := make([]string, 0, len(rawClusters))
	for _, c := range rawClusters {
		trimmed := strings.TrimSpace(c)
		if trimmed != "" {
			selectedClusters = append(selectedClusters, trimmed)
		}
	}

	var rows []domain.ProductSupplyRow
	leadTimeDays := 3.0

	if *filePath != "" {
		slog.Info("Запуск в Offline режиме", "file", *filePath)
		var err error
		rows, err = client.FetchProductsFromJSON(*filePath)
		if err != nil {
			slog.Error("Не удалось прочитать mock JSON", "err", err)
			os.Exit(1)
		}
	} else {
		cfg, err := config.LoadConfig()
		if err != nil {
			slog.Error("Ошибка конфигурации", "err", err)
			os.Exit(1)
		}

		if *resetKeys {
			cfg.ClientID = ""
			cfg.APIKey = ""
		}
		if err := config.ValidateOrPromptKeys(cfg); err != nil {
			slog.Error("Ошибка авторизации Ozon", "err", err)
			os.Exit(1)
		}

		ozon := client.NewOzonClient(cfg.ClientID, cfg.APIKey)
		ctx := context.Background()

		rawMap, err := ozon.FetchAllProducts(ctx, *windowDaysFlag)
		if err != nil {
			slog.Error("Ошибка загрузки данных из Ozon API", "err", err)
			os.Exit(1)
		}

		for _, raw := range rawMap {
			row := domain.CalculateProductRow(
				raw.OfferID,
				raw.Name,
				raw.StockFBS,
				raw.FBOClusters,
				raw.ClusterSales, // Передаем продажи конкретно по каждому кластеру
				selectedClusters,
				*targetDaysFlag,
				*useFBSFlag,
				leadTimeDays,
			)
			rows = append(rows, row)
		}
	}

	printer.PrintSummaryTable(rows, selectedClusters)
}
