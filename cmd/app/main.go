package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"CLIForOZON/internal/client"
	"CLIForOZON/internal/config"
	"CLIForOZON/internal/domain"
	"CLIForOZON/internal/printer"
)

const defaultLeadTimeDays = 3.0

func main() {
	// Значения по умолчанию берем из config.yaml, чтобы флаги и сохраненный
	// конфиг не расходились. Ошибку чтения обрабатываем позже, когда настроен логгер.
	cfg, cfgErr := config.LoadConfig()
	defaultWindowDays, defaultTargetDays := 28, 25
	defaultClusters := "Москва"
	if cfgErr == nil {
		if cfg.WindowDays > 0 {
			defaultWindowDays = cfg.WindowDays
		}
		if cfg.TargetDays > 0 {
			defaultTargetDays = cfg.TargetDays
		}
		if strings.TrimSpace(cfg.Clusters) != "" {
			defaultClusters = cfg.Clusters
		}
	}

	filePath := flag.String("file", "", "Путь к мок-файлу JSON (offline режим)")
	placementFilePath := flag.String("placement-file", "", "Путь к файлу отчета о стоимости размещения (.xlsx/.csv)")
	clustersFlag := flag.String("clusters", defaultClusters, "Целевые кластеры через запятую или 'all' для всех (например: Москва,СПб)")
	windowDaysFlag := flag.Int("prodazhi", defaultWindowDays, "Окно расчета продаж (в днях)")
	targetDaysFlag := flag.Int("postavka", defaultTargetDays, "На сколько дней обеспечить запас")
	resetKeys := flag.Bool("reset-keys", false, "Сбросить сохраненные API ключи Ozon")
	reportTimeoutMin := flag.Int("report-timeout", 5, "Сколько минут ждать готовности отчета Ozon о стоимости размещения")
	debugFlag := flag.Bool("debug", false, "Включить подробный вывод отладки")
	debugOfferFlag := flag.String("debug-offer", "", "Показать все отправления указанного артикула за 90 дней")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *debugFlag {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	if *windowDaysFlag <= 0 {
		slog.Error("Некорректный параметр -prodazhi: значение должно быть больше 0", "prodazhi", *windowDaysFlag)
		os.Exit(1)
	}
	if *targetDaysFlag <= 0 {
		slog.Error("Некорректный параметр -postavka: значение должно быть больше 0", "postavka", *targetDaysFlag)
		os.Exit(1)
	}
	if *reportTimeoutMin <= 0 {
		slog.Error("Некорректный параметр -report-timeout: значение должно быть больше 0", "report_timeout", *reportTimeoutMin)
		os.Exit(1)
	}

	leadTimeDays := defaultLeadTimeDays
	slog.Info("Параметры расчета поставки",
		"запас_дней", *targetDaysFlag,
		"страховой_запас_дней", leadTimeDays,
		"итого_дней_вперед", float64(*targetDaysFlag)+leadTimeDays,
	)

	isAllClusters := strings.EqualFold(strings.TrimSpace(*clustersFlag), "all")
	rawClusters := strings.Split(*clustersFlag, ",")
	selectedClusters := make([]string, 0, len(rawClusters))
	seenClusters := make(map[string]bool, len(rawClusters))
	for _, c := range rawClusters {
		trimmed := client.NormalizeClusterName(c)
		if trimmed != "" {
			if strings.EqualFold(trimmed, "all") {
				isAllClusters = true
			} else if !seenClusters[trimmed] {
				seenClusters[trimmed] = true
				selectedClusters = append(selectedClusters, trimmed)
			}
		}
	}
	if !isAllClusters && len(selectedClusters) == 0 {
		selectedClusters = []string{"Москва"}
	}

	var rows []domain.ProductSupplyRow
	var placementData map[string]client.PlacementItemData
	var placementErr error
	now := time.Now()

	// Загрузка локального файла отчета о размещении при передаче флага
	if *placementFilePath != "" {
		slog.Info("Чтение отчета о размещении из локального файла", "file", *placementFilePath)
		placementData, placementErr = client.ParsePlacementReportFile(*placementFilePath)
		if placementErr != nil {
			slog.Warn("Не удалось разобрать локальный отчет о размещении", "err", placementErr)
		} else {
			slog.Info("Данные о размещении успешно загружены из файла", "позиций", len(placementData))
		}
	}

	if *filePath != "" {
		slog.Info("Запуск в Offline режиме", "file", *filePath)
		var err error
		rows, err = client.FetchProductsFromJSON(*filePath)
		if err != nil {
			slog.Error("Не удалось прочитать mock JSON", "err", err)
			os.Exit(1)
		}

		clusterSet := make(map[string]bool)
		for i := range rows {
			normalized := make(map[string]domain.ClusterStockData, len(rows[i].ClusterData))
			for cluster, data := range rows[i].ClusterData {
				normCluster := client.NormalizeClusterName(cluster)
				clusterSet[normCluster] = true
				if data.PeriodSales == 0 && data.DailySales != 0 {
					data.PeriodSales = data.DailySales * float64(*windowDaysFlag)
				}
				normalized[normCluster] = data
			}
			rows[i].ClusterData = normalized

			// В мок-файле общих итогов может не быть — тогда считаем их по
			// имеющимся кластерам. Прогноз хранения опирается на полный остаток
			// и полные продажи, поэтому пустые итоги дали бы нулевой прогноз.
			if rows[i].TotalFBOStock == 0 && rows[i].TotalPeriodSales == 0 {
				for _, data := range normalized {
					rows[i].TotalFBOStock += data.StockFBO
					rows[i].TotalPeriodSales += data.PeriodSales
				}
			}

			// Привязываем данные о размещении, если они загружены
			if placementData != nil {
				if item, ok := placementData[rows[i].OfferID]; ok {
					attachPlacement(&rows[i], item, *windowDaysFlag, now)
				}
			}
		}

		if isAllClusters {
			selectedClusters = make([]string, 0, len(clusterSet))
			for c := range clusterSet {
				if c != "" {
					selectedClusters = append(selectedClusters, c)
				}
			}
			sort.Strings(selectedClusters)
		}
	} else {
		if cfgErr != nil {
			slog.Error("Ошибка конфигурации", "err", cfgErr)
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

		// Сохраняем выбранные кластеры в config.yaml
		if *clustersFlag != "" && cfg.Clusters != *clustersFlag {
			cfg.Clusters = *clustersFlag
			_ = config.SaveConfig(cfg)
		}

		ozon := client.NewOzonClient(cfg.ClientID, cfg.APIKey)
		ozon.SetReportTimeout(time.Duration(*reportTimeoutMin) * time.Minute)
		ctx := context.Background()

		// Диагностика по одному артикулу: печатаем все отправления за 90 дней,
		// чтобы увидеть расхождение с кабинетом (даты, статусы, кластеры).
		if strings.TrimSpace(*debugOfferFlag) != "" {
			ozon.SetDebugOffer(*debugOfferFlag)
			ozon.DebugOfferPostings(ctx, *windowDaysFlag)
		}

		// Если файл отчета о размещении не передан вручную, запрашиваем его через Ozon API
		if placementData == nil {
			pData, pErr := ozon.FetchPlacementReport(ctx, *windowDaysFlag)
			placementData, placementErr = pData, pErr
			if pErr != nil {
				slog.Warn("Не удалось автоматически получить отчет о стоимости размещения через API (отчет будет сформирован без данных о платном хранении)", "err", pErr)
			} else {
				slog.Info("Отчет о стоимости размещения успешно получен из Ozon API", "позиций", len(placementData))
			}
		}

		rawMap, err := ozon.FetchAllProducts(ctx, *windowDaysFlag)
		if err != nil {
			slog.Error("Ошибка загрузки данных из Ozon API", "err", err)
			os.Exit(1)
		}

		if isAllClusters {
			selectedClusters = ozon.DiscoveredClusters()
			if len(selectedClusters) == 0 {
				selectedClusters = []string{"Москва"}
			}
			slog.Info("Выбраны все обнаруженные кластеры", "clusters", selectedClusters)
		}

		for _, raw := range rawMap {
			row := domain.CalculateProductRow(
				raw.OfferID,
				raw.Name,
				raw.StockFBS,
				raw.FBOClusters,
				raw.ClusterSales,
				raw.PeriodSales,
				selectedClusters,
				*targetDaysFlag,
				leadTimeDays,
			)
			row.TotalFBOStock = raw.TotalFBO
			row.TotalPeriodSales = raw.TotalPeriodSales

			// Привязываем данные о стоимости размещения
			if placementData != nil {
				if item, ok := lookupPlacement(placementData, raw.OfferID, raw.SKU); ok {
					attachPlacement(&row, item, *windowDaysFlag, now)
				}
			}

			rows = append(rows, row)
		}
	}

	slog.Info("Формирование отчета для кластеров", "clusters", selectedClusters)

	// Если данных о размещении нет, отчет не должен выглядеть так, будто
	// хранения не было вовсе: пишем причину в титул и легенду.
	placementNote := ""
	switch {
	case placementErr != nil:
		placementNote = "данные о размещении Ozon не получены: " + shortError(placementErr)
	case placementData == nil || len(placementData) == 0:
		placementNote = "данные о размещении Ozon не получены — колонки хранения пустые"
	case !placementReportHasForecast(placementData):
		slog.Warn("В отчете Ozon нет прогноза стоимости размещения — будет показана оценка программы. " +
			"Точный прогноз Ozon есть в выгрузке ЛК «Стоимость размещения по товарам»: передайте её через -placement-file")
	}

	// Консольная сводка временно отключена: результат выводится только в Excel-отчет.
	// Чтобы вернуть таблицу в консоль, раскомментируйте вызов ниже.
	// printer.PrintSummaryTable(rows, selectedClusters)

	reportPath, err := printer.ExportToExcel(rows, selectedClusters, placementNote, *windowDaysFlag)
	if err != nil {
		slog.Error("Не удалось создать Excel-отчет", "err", err)
		os.Exit(1)
	}
	slog.Info("Excel-отчет сохранен", "file", reportPath)
}

// placementReportHasForecast сообщает, есть ли в отчете прогноз Ozon: он
// присутствует только в выгрузке личного кабинета.
func placementReportHasForecast(data map[string]client.PlacementItemData) bool {
	for _, item := range data {
		if item.HasOzonForecast {
			return true
		}
	}
	return false
}

// shortError обрезает текст ошибки: в титуле отчета помещается только строка
// разумной длины, полный текст остается в логе.
func shortError(err error) string {
	const limit = 140
	text := err.Error()
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// lookupPlacement находит строку отчета Ozon по артикулу, затем по SKU.
func lookupPlacement(data map[string]client.PlacementItemData, offerID, sku string) (client.PlacementItemData, bool) {
	if item, ok := data[offerID]; ok {
		return item, true
	}
	if sku != "" {
		if item, ok := data[sku]; ok {
			return item, true
		}
	}
	return client.PlacementItemData{}, false
}

// attachPlacement переносит данные отчета о размещении в строку отчета и
// считает прогноз хранения на будущее.
//
// Прошлая сумма списаний в отчет не попадает: она говорит о том, что уже
// случилось. Если товар распродан, размещения и будущих списаний нет, поэтому
// статус размещения очищается — иначе строка «Платно» с нулевым остатком
// вводит в заблуждение.
func attachPlacement(row *domain.ProductSupplyRow, item client.PlacementItemData, windowDays int, now time.Time) {
	row.PlacementInfo = item.PlacementInfo
	row.StorageCost = item.PastStorageCost
	row.OzonStorageForecast28 = item.OzonForecast28
	row.OzonStorageForecastMonth = item.OzonForecastMonth
	row.HasOzonStorageForecast = item.HasOzonForecast
	row.StorageForecast = domain.StorageForecast(
		row.TotalFBOStock,
		row.TotalPeriodSales,
		item.PastStorageCost,
		windowDays,
		domain.DefaultStorageForecastDays,
		item.FreeUntil,
		now,
	)
	if row.TotalFBOStock == 0 {
		row.PlacementInfo = ""
	}
}
