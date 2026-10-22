// Package client — интеграция с Ozon Seller API.
//
// Пакет остается одним Go-пакетом (общее состояние и общий ограничитель
// частоты), но разложен по файлам по слоям, чтобы каждый файл читался отдельно:
//
//	ozon.go            — фасад OzonClient: состояние и конструктор;
//	transport.go       — HTTP-транспорт: заголовки, троттлинг, повторы, ошибки;
//	endpoints.go       — пути методов Ozon;
//	products.go        — каталог, SKU, остатки FBS;
//	fbo_stocks.go      — остатки FBO по кластерам (жесткий лимит частоты);
//	clusters.go        — нормализация кластеров и «склад → кластер»;
//	postings.go        — история отправлений и продажи по кластерам;
//	postings_debug.go  — диагностика по одному артикулу;
//	reports.go         — асинхронные отчеты Ozon;
//	placement.go       — отчет о размещении: загрузка и выбор формата;
//	placement_table.go — разбор таблицы размещения;
//	offline.go         — offline-режим из локального JSON.
package client

import (
	"net/http"
	"time"
)

const defaultBaseURL = "https://api-seller.ozon.ru"

// OzonClient — фасад интеграции с Ozon Seller API.
//
// Транспорт (паузы, повторы, заголовки) и доменные загрузки живут в разных
// файлах пакета, но состояние общее: карта «склад → кластер» и кластеры с
// заказами нужны следующему этапу расчета поставок.
type OzonClient struct {
	clientID   string
	apiKey     string
	httpClient *http.Client
	// baseURL позволяет подменить адрес API в тестах; в работе всегда defaultBaseURL.
	baseURL string
	// disableThrottle отключает паузы между запросами (используется только в тестах).
	disableThrottle bool
	// requestOpts задает режим повторов; в тестах заменяется на быстрый.
	requestOpts *sendRequestOpts
	// warehouseToCluster кэширует соответствие «склад -> кластер», полученное из /v1/analytics/stocks.
	warehouseToCluster map[string]string
	// salesClusters — кластеры, в которые были заказы. Нужны для -clusters all:
	// в кластере может быть спрос, но не быть остатков.
	salesClusters map[string]bool
	// debugOffer — артикул, по которому нужен подробный список отправлений
	// (флаг -debug-offer): без него расхождение с кабинетом не разобрать.
	debugOffer string
	// reportTimeout — сколько ждать готовности отчета Ozon (по умолчанию
	// DefaultReportTimeout, меняется флагом -report-timeout).
	reportTimeout time.Duration
}

// NewOzonClient создает клиент с продакшн-настройками: адрес Ozon и таймаут
// HTTP 60 секунд.
func NewOzonClient(clientID, apiKey string) *OzonClient {
	return &OzonClient{
		clientID: clientID,
		apiKey:   apiKey,
		httpClient: &http.Client{
			// Ozon иногда отвечает медленно на больших каталогах: 60 секунд
			// дают запас, не растягивая ожидание при реальных сбоях.
			Timeout: 60 * time.Second,
		},
		baseURL:            defaultBaseURL,
		warehouseToCluster: make(map[string]string),
		salesClusters:      make(map[string]bool),
		reportTimeout:      DefaultReportTimeout,
	}
}

// optsFor возвращает режим повторов для метода: у /v1/analytics/stocks он
// строже из-за жесткого лимита частоты.
func (c *OzonClient) optsFor(path string) sendRequestOpts {
	if c.requestOpts != nil {
		return *c.requestOpts
	}
	if path == pathAnalyticsStocks {
		return analyticsStocksOpts
	}
	return defaultRequestOpts
}

func (c *OzonClient) endpoint(path string) string {
	return c.baseURL + path
}
