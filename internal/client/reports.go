package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// DefaultReportTimeout — сколько по умолчанию ждать готовности отчета Ozon.
// Отчеты формируются асинхронно, на большом каталоге это занимает минуты.
const DefaultReportTimeout = 5 * time.Minute

// SetReportTimeout задает, сколько ждать готовности отчета Ozon.
func (c *OzonClient) SetReportTimeout(d time.Duration) {
	if d > 0 {
		c.reportTimeout = d
	}
}

// retryPause задает паузу перед повтором. В тестах она сокращается вместе с
// остальными паузами (requestOpts), чтобы тесты не ждали десятки секунд.
func (c *OzonClient) retryPause(def time.Duration) time.Duration {
	if c.requestOpts != nil && c.requestOpts.baseWait > 0 {
		return c.requestOpts.baseWait
	}
	return def
}

// FetchPlacementReport запрашивает отчет о размещении у Ozon.
//
// Отчет формируется асинхронно: сначала создается задание, затем файл
// дожидается через /v1/report/info. Устойчивость обеспечена на трех уровнях:
// повтор создания, ожидание до reportTimeout с растущей паузой опроса и
// повторное скачивание (недокачанный или не разобранный файл перекачивается).
// Если не удался весь цикл, он повторяется целиком: Ozon иногда роняет задание,
// и новое создается нормально.
func (c *OzonClient) FetchPlacementReport(ctx context.Context, windowDays int) (map[string]PlacementItemData, error) {
	if windowDays <= 0 {
		windowDays = 28
	}
	timeout := c.reportTimeout
	if timeout <= 0 {
		timeout = DefaultReportTimeout
	}

	const attempts = 2
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		data, err := c.fetchPlacementReportOnce(ctx, windowDays, timeout)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if attempt < attempts {
			slog.Warn("Отчет о размещении не получен, повтор запроса", "attempt", attempt, "ожидание", timeout, "err", err)
			if sleepErr := sleepContext(ctx, c.retryPause(10*time.Second)); sleepErr != nil {
				return nil, sleepErr
			}
		}
	}
	return nil, lastErr
}

// reportCreateReq — задание на отчет о размещении.
type reportCreateReq struct {
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	Language string `json:"language,omitempty"`
}

type reportCreateResp struct {
	Code   string `json:"code"`
	Result struct {
		Code string `json:"code"`
	} `json:"result"`
}

type reportInfoReq struct {
	Code string `json:"code"`
}

type reportInfoResp struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Error  string `json:"error"`
	File   string `json:"file"`
	Result struct {
		Code   string `json:"code"`
		Status string `json:"status"`
		Error  string `json:"error"`
		File   string `json:"file"`
	} `json:"result"`
}

// fetchPlacementReportOnce — один цикл «создать → дождаться → скачать → разобрать».
func (c *OzonClient) fetchPlacementReportOnce(ctx context.Context, windowDays int, timeout time.Duration) (map[string]PlacementItemData, error) {
	slog.Info("Запрос отчета о стоимости размещения товаров на складах Ozon...",
		"период_дней", windowDays, "ожидание", timeout)

	code, err := c.createPlacementReport(ctx, windowDays)
	if err != nil {
		return nil, err
	}
	slog.Info("Отчет о стоимости размещения поставлен в очередь Ozon", "code", code)

	pollInterval := 3 * time.Second
	if c.requestOpts != nil && c.requestOpts.baseWait > 0 {
		pollInterval = c.requestOpts.baseWait
	}
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("отчет о размещении не готов за %v (код %s)", timeout, code)
		}
		if err := sleepContext(ctx, pollInterval); err != nil {
			return nil, err
		}

		var infoResp reportInfoResp
		if err := c.sendRequest(ctx, pathReportInfo, reportInfoReq{Code: code}, &infoResp); err != nil {
			// Сетевой сбой или лимит частоты не повод бросать задание: файл
			// продолжает формироваться, поэтому просто опрашиваем дальше.
			slog.Warn("Ошибка проверки статуса отчета", "err", err)
		} else {
			status := infoResp.Result.Status
			if status == "" {
				status = infoResp.Status
			}
			file := infoResp.Result.File
			if file == "" {
				file = infoResp.File
			}
			errMsg := infoResp.Result.Error
			if errMsg == "" {
				errMsg = infoResp.Error
			}
			slog.Debug("Статус формирования отчета о стоимости размещения", "status", status)

			switch status {
			case "success":
				if file == "" {
					return nil, fmt.Errorf("Ozon вернул отчет без ссылки на файл")
				}
				return c.downloadPlacementReport(ctx, file)
			case "failed":
				return nil, fmt.Errorf("генерация отчета Ozon завершилась с ошибкой: %s", errMsg)
			}
		}

		// Растущая пауза: частые опросы бесполезны, а лимит частоты у Ozon общий.
		if pollInterval < 15*time.Second {
			pollInterval += 2 * time.Second
		}
	}
}

// createPlacementReport создает задание на отчет и возвращает его код.
func (c *OzonClient) createPlacementReport(ctx context.Context, windowDays int) (string, error) {
	now := time.Now()
	reqPayload := reportCreateReq{
		DateFrom: now.AddDate(0, 0, -windowDays).Format("2006-01-02"),
		DateTo:   now.Format("2006-01-02"),
		Language: "DEFAULT",
	}

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		var createResp reportCreateResp
		err := c.sendRequest(ctx, pathReportPlacementByProductsCreate, reqPayload, &createResp)
		if err == nil {
			code := createResp.Result.Code
			if code == "" {
				code = createResp.Code
			}
			if code != "" {
				return code, nil
			}
			err = fmt.Errorf("пустой код отчета о стоимости размещения от Ozon")
		}
		lastErr = err
		if attempt < 2 {
			slog.Warn("Создание отчета о размещении не удалось, повтор", "err", err)
			if sleepErr := sleepContext(ctx, c.retryPause(5*time.Second)); sleepErr != nil {
				return "", sleepErr
			}
		}
	}
	return "", fmt.Errorf("создание отчета о стоимости размещения не удалось: %w", lastErr)
}

// downloadPlacementReport скачивает и разбирает файл отчета. Ссылка Ozon живет
// несколько минут, поэтому недокачанный или не разобранный файл перекачивается.
func (c *OzonClient) downloadPlacementReport(ctx context.Context, fileURL string) (map[string]PlacementItemData, error) {
	parsedURL, err := url.Parse(fileURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return nil, fmt.Errorf("некорректный URL файла отчета: %q", fileURL)
	}

	slog.Info("Отчет о размещении сформирован Ozon, скачивание...", "url", fileURL)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		items, err := c.downloadPlacementReportOnce(ctx, fileURL)
		if err == nil {
			return items, nil
		}
		lastErr = err
		if attempt < 3 {
			slog.Warn("Скачивание отчета о размещении не удалось, повтор", "attempt", attempt, "err", err)
			if sleepErr := sleepContext(ctx, c.retryPause(3*time.Second)); sleepErr != nil {
				return nil, sleepErr
			}
		}
	}
	return nil, lastErr
}

func (c *OzonClient) downloadPlacementReportOnce(ctx context.Context, fileURL string) (map[string]PlacementItemData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса на скачивание отчета: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка скачивания файла отчета: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("не удалось скачать файл отчета, статус HTTP: %d", resp.StatusCode)
	}

	data, err := readBoundedBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения тела файла отчета: %w", err)
	}

	items, err := ParsePlacementReportData(data, fileURL)
	if err != nil {
		return nil, fmt.Errorf("не удалось разобрать файл отчета: %w", err)
	}
	return items, nil
}
