package client

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"CLIForOZON/internal/domain"
)

const defaultBaseURL = "https://api-seller.ozon.ru"

// maxResponseBytes ограничивает размер тела ответа от API/отчетов (64 МБ),
// защищая процесс от исчерпания памяти при некорректном ответе сервера.
const maxResponseBytes = 64 << 20

func readBoundedBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response body exceeds limit of %d bytes", maxResponseBytes)
	}
	return data, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Пути методов Ozon. Держим их рядом, чтобы троттлинг и запросы использовали
// один и тот же ключ.
const (
	pathProductList                     = "/v3/product/list"
	pathProductInfoList                 = "/v3/product/info/list"
	pathAnalyticsStocks                 = "/v1/analytics/stocks"
	pathPostingFBOList                  = "/v3/posting/fbo/list"
	pathProductInfoStocks               = "/v4/product/info/stocks"
	pathReportPlacementByProductsCreate = "/v1/report/placement/by-products/create"
	pathReportInfo                      = "/v1/report/info"
)

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
	// reportTimeout — сколько ждать готовности отчета Ozon (по умолчанию
	// DefaultReportTimeout, меняется флагом -report-timeout).
	reportTimeout time.Duration
}

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

// minRequestInterval — общий лимит Ozon: 2 запроса в секунду на Client-Id.
// Переменная (а не константа) нужна, чтобы тесты могли отключить паузу.
var minRequestInterval = 600 * time.Millisecond

// slowEndpointIntervals задает увеличенную паузу для методов с более жестким
// лимитом частоты. Для /v1/analytics/stocks документация допускает примерно
// один запрос в минуту, поэтому без этой паузы запросы стабильно упираются в 429.
var slowEndpointIntervals = map[string]time.Duration{
	pathAnalyticsStocks: 6 * time.Second,
}

var (
	throttleMu   sync.Mutex
	lastCallTime time.Time
	lastSlowCall = make(map[string]time.Time)
)

// throttle выдерживает паузу между запросами, чтобы не упираться в лимит Ozon.
// Лимит общий на кабинет, поэтому ограничитель общий для процесса и удерживает
// блокировку на время ожидания — запросы идут строго последовательно.
func throttle(path string) {
	throttleMu.Lock()
	defer throttleMu.Unlock()

	now := time.Now()
	wait := minRequestInterval - now.Sub(lastCallTime)

	slow, isSlow := slowEndpointIntervals[path]
	if isSlow {
		if slowWait := slow - now.Sub(lastSlowCall[path]); slowWait > wait {
			wait = slowWait
		}
	}

	if wait > 0 {
		time.Sleep(wait)
	}

	now = time.Now()
	lastCallTime = now
	if isSlow {
		lastSlowCall[path] = now
	}
}

// sendRequestOpts настраивает повторы запроса для методов с жесткими лимитами.
type sendRequestOpts struct {
	maxAttempts int
	// baseWait — пауза перед первым повтором; далее она растет экспоненциально.
	baseWait time.Duration
	// maxWait ограничивает паузу между повторами.
	maxWait time.Duration
}

// defaultRequestOpts подходит большинству методов Ozon.
var defaultRequestOpts = sendRequestOpts{
	maxAttempts: 5,
	baseWait:    1500 * time.Millisecond,
	maxWait:     15 * time.Second,
}

// analyticsStocksOpts задает более осторожный режим для /v1/analytics/stocks:
// у метода жесткий лимит частоты, поэтому паузы длиннее, а повторов больше.
// Более длинные паузы, чем у обычных методов, нужны, чтобы пережить лимит
// частоты, а не добить его новыми запросами.
var analyticsStocksOpts = sendRequestOpts{
	maxAttempts: 5,
	baseWait:    4 * time.Second,
	maxWait:     45 * time.Second,
}

func retryWait(opts sendRequestOpts, attempt int) time.Duration {
	wait := opts.baseWait * time.Duration(1<<uint(attempt-1))
	if wait > opts.maxWait {
		wait = opts.maxWait
	}
	return wait
}

// isRetryableStatus определяет, стоит ли повторять запрос. 5xx у Ozon бывают
// транзиентными (в ответе приходит code 2), а 429 означает упор в лимит частоты.
func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// sendRequest отправляет запрос к методу Ozon по относительному пути
// (например, pathProductList) и применяет стандартные повторы.
func (c *OzonClient) sendRequest(ctx context.Context, path string, reqPayload, respTarget any) error {
	return c.sendRequestWithOpts(ctx, path, reqPayload, respTarget, c.optsFor(path))
}

func (c *OzonClient) sendRequestWithOpts(ctx context.Context, path string, reqPayload, respTarget any, opts sendRequestOpts) error {
	var bodyBytes []byte
	var err error
	if reqPayload != nil {
		bodyBytes, err = json.Marshal(reqPayload)
		if err != nil {
			return fmt.Errorf("marshal request failed: %w", err)
		}
	}

	endpoint := c.endpoint(path)
	var lastErr error
	for attempt := 1; attempt <= opts.maxAttempts; attempt++ {
		if !c.disableThrottle {
			throttle(path)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("create request failed: %w", err)
		}

		req.Header.Set("Client-Id", c.clientID)
		req.Header.Set("Api-Key", c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http request error: %w", err)
			if attempt < opts.maxAttempts && ctx.Err() == nil {
				wait := retryWait(opts, attempt)
				slog.Warn("Сетевая ошибка Ozon, повтор запроса", "attempt", attempt, "wait", wait, "err", err)
				if err := sleepContext(ctx, wait); err != nil {
					return err
				}
				continue
			}
			return lastErr
		}

		respBytes, err := readBoundedBody(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response body failed: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			if respTarget != nil {
				if err := json.Unmarshal(respBytes, respTarget); err != nil {
					return fmt.Errorf("unmarshal response error: %w", err)
				}
			}
			return nil
		}

		lastErr = fmt.Errorf("ozon api error (status %d): %s", resp.StatusCode, string(respBytes))

		if isRetryableStatus(resp.StatusCode) && attempt < opts.maxAttempts {
			wait := retryWait(opts, attempt)
			slog.Warn("Временная ошибка Ozon, повтор запроса",
				"status", resp.StatusCode, "attempt", attempt, "max", opts.maxAttempts, "wait", wait)
			if err := sleepContext(ctx, wait); err != nil {
				return err
			}
			continue
		}

		return lastErr
	}

	return fmt.Errorf("max retries exceeded for %s: %w", path, lastErr)
}

type productListReq struct {
	Filter struct {
		Visibility string `json:"visibility"`
	} `json:"filter"`
	LastID string `json:"last_id,omitempty"`
	Limit  int    `json:"limit"`
}

type productListResp struct {
	Result struct {
		Items []struct {
			ProductID int64  `json:"product_id"`
			OfferID   string `json:"offer_id"`
		} `json:"items"`
		LastID string `json:"last_id"`
	} `json:"result"`
}

// productInfoListReq — запрос к /v3/product/info/list. Метод принимает до 1000
// идентификаторов за вызов и возвращает в том числе SKU и название товара.
type productInfoListReq struct {
	OfferID []string `json:"offer_id,omitempty"`
}

type productInfoListResp struct {
	Items []struct {
		OfferID string `json:"offer_id"`
		SKU     int64  `json:"sku"`
		Name    string `json:"name"`
	} `json:"items"`
}

// analyticsStocksReq — запрос к /v1/analytics/stocks. Метод требует явный
// список SKU (не более 100 за вызов), поэтому вызывается пачками.
type analyticsStocksReq struct {
	SKUs []string `json:"skus"`
}

// analyticsStocksResp — ответ /v1/analytics/stocks. Зерно данных — SKU × склад,
// в каждой строке есть название кластера. Поле available_stock_count совпадает
// с колонкой «Доступно к продаже» в личном кабинете Ozon, поэтому оно заменяет
// устаревший free_to_sell_amount из /v2/analytics/stock_on_warehouses.
type analyticsStocksResp struct {
	Items []struct {
		SKU                 int64  `json:"sku"`
		OfferID             string `json:"offer_id"`
		Name                string `json:"name"`
		WarehouseName       string `json:"warehouse_name"`
		ClusterName         string `json:"cluster_name"`
		AvailableStockCount int    `json:"available_stock_count"`
	} `json:"items"`
}

type productStocksV4Req struct {
	Filter struct {
		OfferID    []string `json:"offer_id,omitempty"`
		Visibility string   `json:"visibility,omitempty"`
	} `json:"filter"`
	Limit int `json:"limit"`
}

type productStocksV4Resp struct {
	Items []struct {
		OfferID string `json:"offer_id"`
		Stocks  []struct {
			Type    string `json:"type"`
			Present int    `json:"present"`
		} `json:"stocks"`
	} `json:"items"`
}

type RawProductData struct {
	OfferID      string
	SKU          string
	Name         string
	StockFBS     int
	FBOClusters  map[string]int
	ClusterSales map[string]float64 // Продажи в день для расчётов
	PeriodSales  map[string]float64 // Продажи за весь период
	// TotalFBO — остаток на всех складах Ozon без двойного счета: нужен для
	// прогноза хранения, которое начисляется за весь остаток, а не только за
	// выбранные кластеры.
	TotalFBO int
	// TotalPeriodSales — продажи за весь период по всем складам. Вместе с
	// остатком задает средний остаток, от которого оценивается тариф хранения.
	TotalPeriodSales float64
}

func (c *OzonClient) FetchAllProducts(ctx context.Context, windowDays int) (map[string]*RawProductData, error) {
	slog.Info("Загрузка полного каталога товаров селлера...")
	productsMap := make(map[string]*RawProductData)
	var offerIDs []string
	lastID := ""

	for {
		var listResp productListResp
		req := productListReq{
			Filter: struct {
				Visibility string `json:"visibility"`
			}{Visibility: "ALL"},
			LastID: lastID,
			Limit:  1000,
		}

		err := c.sendRequest(ctx, pathProductList, req, &listResp)
		if err != nil {
			return nil, fmt.Errorf("fetch products batch failed: %w", err)
		}

		for _, item := range listResp.Result.Items {
			offerIDs = append(offerIDs, item.OfferID)
			productsMap[item.OfferID] = &RawProductData{
				OfferID:      item.OfferID,
				FBOClusters:  make(map[string]int),
				ClusterSales: make(map[string]float64),
				PeriodSales:  make(map[string]float64),
			}
		}

		if len(listResp.Result.Items) == 0 || listResp.Result.LastID == "" || listResp.Result.LastID == lastID {
			break
		}
		lastID = listResp.Result.LastID
	}

	slog.Info("Всего товаров получено", "count", len(productsMap))

	slog.Info("Загрузка SKU и названий товаров...")
	if err := c.loadProductSKUs(ctx, offerIDs, productsMap); err != nil {
		// Без SKU невозможно запросить остатки FBO, поэтому ошибка критична.
		return nil, fmt.Errorf("критическая ошибка загрузки SKU товаров: %w", err)
	}

	slog.Info("Загрузка FBO остатков по кластерам...")
	warehouseToCluster, coveredSKUs, err := c.loadFBOStocks(ctx, productsMap)
	if err != nil {
		// Остатки FBO — основа расчета поставок. Продолжать с неполными
		// данными нельзя: это даст завышенные поставки и нулевые остатки,
		// поэтому ошибка критична и прерывает работу.
		return nil, fmt.Errorf("критическая ошибка загрузки остатков FBO: %w", err)
	}

	// Дополнительно проверяем, что данные пришли по всем товарам, у которых
	// есть SKU: иначе часть позиций молча получила бы нулевой остаток.
	var uncovered []string
	for offerID, prod := range productsMap {
		if prod.SKU != "" && !coveredSKUs[prod.SKU] {
			uncovered = append(uncovered, offerID)
		}
	}
	if len(uncovered) > 0 {
		sample := uncovered
		if len(sample) > 5 {
			sample = sample[:5]
		}
		return nil, fmt.Errorf(
			"остатки FBO не получены для %d товаров (например: %s) — расчет остановлен во избежание неверных поставок",
			len(uncovered), strings.Join(sample, ", "))
	}

	slog.Info("Загрузка FBS остатков...")
	offerIndex := buildOfferIndex(productsMap)
	chunkSize := 100
	var fbsFailed int
	for i := 0; i < len(offerIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(offerIDs) {
			end = len(offerIDs)
		}
		if err := c.loadFBSStocksChunk(ctx, offerIDs[i:end], productsMap, offerIndex); err != nil {
			fbsFailed += end - i
			slog.Warn("Не удалось получить FBS остатки для пакета", "from", i, "to", end, "err", err)
		}
	}
	if fbsFailed > 0 {
		// FBS остаток влияет на статус (OUT_OF_STOCK/NEEDS_FBS), поэтому
		// неполные данные дали бы неверную картину по складу продавца.
		return nil, fmt.Errorf("FBS остатки не получены для %d товаров — расчет остановлен", fbsFailed)
	}

	slog.Info("Загрузка аналитики продаж по кластерам...", "За период:", windowDays)
	if err := c.loadSalesVelocityByCluster(ctx, windowDays, productsMap, warehouseToCluster); err != nil {
		// Продажи задают целевой запас: при неполной истории поставки будут
		// занижены, поэтому ошибка критична.
		return nil, fmt.Errorf("критическая ошибка загрузки аналитики продаж: %w", err)
	}

	return productsMap, nil
}

var moscowKeywords = []string{
	"вешки",
	"ватутинки",
	"гривно",
	"домодедово",
	"жуковский",
	"купавна",
	"никольское",
	"ногинск",
	"павло слободское",
	"павловская слобода",
	"петровское",
	"петровск",
	"пушкино",
	"радумля",
	"софьино",
	"хоругвино",
	"москва",
}

// isMoscowWarehouse оставлен как резервный матчер: он нужен, когда склад из
// истории отправлений отсутствует в карте «склад → кластер», полученной из
// /v1/analytics/stocks.
func isMoscowWarehouse(name string) bool {
	n := strings.ToLower(name)
	for _, kw := range moscowKeywords {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

// clusterAliases приводит названия кластеров Ozon к коротким именам, которые
// используются во флагах CLI, config.yaml и offline-мок-файлах.
var clusterAliases = map[string]string{
	"москва, мо и дальние регионы": "Москва",
	"москва": "Москва",
	"санкт-петербург и сзо": "Санкт-Петербург",
	"санкт-петербург":       "Санкт-Петербург",
	"спб":                   "Санкт-Петербург",
	"питер":                 "Санкт-Петербург",
	"поволжье":              "Поволжье",
	"казань":                "Поволжье",
	"самара":                "Поволжье",
	"юг":                    "Юг",
	"ростов-на-дону":        "Юг",
	"ростов":                "Юг",
	"краснодар":             "Юг",
	"урал":                  "Урал",
	"екатеринбург":          "Урал",
	"сибирь":                "Сибирь",
	"новосибирск":           "Сибирь",
	"дальний восток":        "Дальний Восток",
	"хабаровск":             "Дальний Восток",
	"владивосток":           "Дальний Восток",
	"воронеж":               "Воронеж",
	"калининград":           "Калининград",
	"северный кавказ":       "Северный Кавказ",
	"беларусь":              "Беларусь",
	"казахстан":             "Казахстан",
}

// NormalizeClusterName приводит название кластера Ozon к короткому имени,
// которое используется в CLI. Применяется и к данным из API, и к списку
// кластеров из флага -clusters, чтобы ключи совпадали.
func NormalizeClusterName(name string) string {
	trimmed := strings.TrimSpace(name)
	if alias, ok := clusterAliases[strings.ToLower(trimmed)]; ok {
		return alias
	}
	return trimmed
}

// matchWarehouseClusterFallback используется для складов, отсутствующих
// в карте «склад → кластер», полученной из остатков FBO.
func matchWarehouseClusterFallback(whName string) string {
	n := strings.ToLower(whName)
	if isMoscowWarehouse(n) {
		return "Москва"
	}
	if strings.Contains(n, "петербург") || strings.Contains(n, "шушары") || strings.Contains(n, "бугры") || strings.Contains(n, "утконос спб") {
		return "Санкт-Петербург"
	}
	if strings.Contains(n, "казань") || strings.Contains(n, "поволжье") || strings.Contains(n, "самара") {
		return "Поволжье"
	}
	if strings.Contains(n, "ростов") || strings.Contains(n, "краснодар") || strings.Contains(n, "невинномысск") || strings.Contains(n, "адыгея") {
		return "Юг"
	}
	if strings.Contains(n, "екатеринбург") || strings.Contains(n, "урал") || strings.Contains(n, "пермь") {
		return "Урал"
	}
	if strings.Contains(n, "новосибирск") || strings.Contains(n, "сибирь") || strings.Contains(n, "красноярск") {
		return "Сибирь"
	}
	if strings.Contains(n, "хабаровск") || strings.Contains(n, "владивосток") || strings.Contains(n, "дальний восток") {
		return "Дальний Восток"
	}
	return ""
}

// DiscoverClusters возвращает отсортированный список уникальных кластеров
// из карты соответствия складов.
func DiscoverClusters(warehouseToCluster map[string]string) []string {
	seen := make(map[string]bool)
	var clusters []string
	for _, c := range warehouseToCluster {
		normalized := NormalizeClusterName(c)
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			clusters = append(clusters, normalized)
		}
	}
	sort.Strings(clusters)
	return clusters
}

// DiscoveredClusters возвращает список обнаруженных кластеров в текущем клиенте.
func (c *OzonClient) DiscoveredClusters() []string {
	return DiscoverClusters(c.warehouseToCluster)
}

// normalizeWarehouseKey нужен, потому что разные методы Ozon отдают названия
// складов в разном регистре (например, «Новосибирск_РФЦ_НОВЫЙ» и
// «НОВОСИБИРСК_РФЦ_НОВЫЙ»).
func normalizeWarehouseKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}

func buildOfferIndex(productsMap map[string]*RawProductData) map[string]*RawProductData {
	idx := make(map[string]*RawProductData, len(productsMap))
	for k, v := range productsMap {
		idx[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return idx
}

func lookupProduct(productsMap, offerIndex map[string]*RawProductData, offerID string) *RawProductData {
	target := strings.TrimSpace(offerID)
	if target == "" {
		return nil
	}
	if prod, ok := productsMap[target]; ok {
		return prod
	}
	if offerIndex != nil {
		return offerIndex[strings.ToLower(target)]
	}
	for k, v := range productsMap {
		if strings.EqualFold(k, target) {
			return v
		}
	}
	return nil
}

// loadProductSKUs заполняет SKU и названия товаров через /v3/product/info/list.
// Метод нужен, потому что /v3/product/list возвращает только offer_id и
// product_id, а /v1/analytics/stocks принимает только SKU.
func (c *OzonClient) loadProductSKUs(ctx context.Context, offerIDs []string, productsMap map[string]*RawProductData) error {
	offerIndex := buildOfferIndex(productsMap)
	const chunkSize = 1000
	for i := 0; i < len(offerIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(offerIDs) {
			end = len(offerIDs)
		}

		var resp productInfoListResp
		err := c.sendRequest(ctx, pathProductInfoList, productInfoListReq{
			OfferID: offerIDs[i:end],
		}, &resp)
		if err != nil {
			return fmt.Errorf("пакет %d-%d: %w", i, end, err)
		}

		for _, item := range resp.Items {
			prod := lookupProduct(productsMap, offerIndex, item.OfferID)
			if prod == nil {
				continue
			}
			if item.SKU != 0 {
				prod.SKU = strconv.FormatInt(item.SKU, 10)
			}
			if item.Name != "" {
				prod.Name = item.Name
			}
		}
	}
	return nil
}

// loadFBOStocks загружает доступные к продаже остатки FBO через
// /v1/analytics/stocks. Этот метод соответствует разделу «FBO → Управление
// остатками» в личном кабинете, поэтому его available_stock_count совпадает с
// колонкой «Доступно к продаже» на сайте.
//
// Устаревший /v2/analytics/stock_on_warehouses не используется: Ozon помечает
// его как deprecated, он отдает остатки без кластеров и расходится с кабинетом.
//
// Метод требует явный список SKU (максимум 100 за вызов), возвращает зерно
// SKU × склад и держит жесткий лимит частоты, поэтому запросы идут пачками с
// длинными паузами, а неудавшиеся пачки повторяются отдельными проходами.
//
// Возвращает карту «склад → кластер» и список SKU, по которым данные получены.
// Второе значение нужно, чтобы отличить «остатков нет» от «данные не доехали»:
// считать поставки по неполным данным нельзя.
func (c *OzonClient) loadFBOStocks(ctx context.Context, productsMap map[string]*RawProductData) (map[string]string, map[string]bool, error) {
	warehouseToCluster := make(map[string]string)
	coveredSKUs := make(map[string]bool)
	offerIndex := buildOfferIndex(productsMap)

	skus := make([]string, 0, len(productsMap))
	skuToOffer := make(map[string]string, len(productsMap))
	for offerID, prod := range productsMap {
		if prod.SKU != "" {
			skus = append(skus, prod.SKU)
			skuToOffer[prod.SKU] = offerID
		}
	}
	if len(skus) == 0 {
		return warehouseToCluster, coveredSKUs, fmt.Errorf("нет SKU товаров для запроса остатков FBO")
	}

	const chunkSize = 100
	chunks := make([][]string, 0, (len(skus)+chunkSize-1)/chunkSize)
	for i := 0; i < len(skus); i += chunkSize {
		end := i + chunkSize
		if end > len(skus) {
			end = len(skus)
		}
		chunks = append(chunks, skus[i:end])
	}

	// Даже после повторов внутри sendRequestWithOpts пачка может не доехать.
	// Тогда пробуем ее отдельными проходами: временные 500 и лимиты частоты
	// обычно рассасываются за время обработки остальных пачек.
	const maxPasses = 2
	pending := chunks
	var lastErr error

	for pass := 1; pass <= maxPasses && len(pending) > 0; pass++ {
		failed := make([][]string, 0)
		for _, chunk := range pending {
			var resp analyticsStocksResp
			err := c.sendRequestWithOpts(ctx, pathAnalyticsStocks, analyticsStocksReq{
				SKUs: chunk,
			}, &resp, c.optsFor(pathAnalyticsStocks))
			if err != nil {
				lastErr = err
				slog.Warn("Не удалось получить пакет остатков FBO",
					"проход", pass, "sku_в_пакете", len(chunk), "err", err)
				failed = append(failed, chunk)
				continue
			}

			for _, sku := range chunk {
				coveredSKUs[sku] = true
			}

			for _, item := range resp.Items {
				prod := lookupProduct(productsMap, offerIndex, item.OfferID)
				if prod == nil && item.SKU != 0 {
					if offerID, ok := skuToOffer[strconv.FormatInt(item.SKU, 10)]; ok {
						prod = productsMap[offerID]
					}
				}
				if prod == nil {
					continue
				}

				if prod.SKU == "" && item.SKU != 0 {
					prod.SKU = strconv.FormatInt(item.SKU, 10)
				}
				if prod.Name == "" || prod.Name == prod.OfferID {
					prod.Name = item.Name
				}

				cluster := NormalizeClusterName(item.ClusterName)
				if cluster == "" {
					cluster = item.WarehouseName
				}

				// Остаток кластера суммируется по всем его складам, включая ПВЗ,
				// куда попадают возвраты от покупателей.
				if cluster != "" {
					prod.FBOClusters[cluster] += item.AvailableStockCount
				}
				if item.WarehouseName != "" {
					if item.WarehouseName != cluster {
						prod.FBOClusters[item.WarehouseName] += item.AvailableStockCount
					}
					warehouseToCluster[normalizeWarehouseKey(item.WarehouseName)] = cluster
				}
				// Общий остаток считаем ровно один раз на строку ответа, иначе
				// остаток склада-кластера попал бы в сумму дважды.
				prod.TotalFBO += item.AvailableStockCount
			}
		}

		pending = failed
		if len(pending) > 0 && pass < maxPasses {
			slog.Warn("Повторная попытка загрузить остатки FBO",
				"пакетов_осталось", len(pending), "следующий_проход", pass+1)
		}
	}

	slog.Info("FBO остатки загружены",
		"складов_в_справочнике", len(warehouseToCluster),
		"sku_покрыто", len(coveredSKUs),
		"sku_всего", len(skus))

	if len(pending) > 0 {
		missing := 0
		for _, chunk := range pending {
			missing += len(chunk)
		}
		return warehouseToCluster, coveredSKUs, fmt.Errorf(
			"остатки FBO получены не для всех товаров: пропущено %d SKU из %d после %d проходов: %w",
			missing, len(skus), maxPasses, lastErr)
	}

	c.warehouseToCluster = warehouseToCluster
	return warehouseToCluster, coveredSKUs, nil
}

type fboPostingListV3Req struct {
	Dir    string `json:"dir"`
	Filter struct {
		Since string `json:"since"`
		To    string `json:"to"`
	} `json:"filter"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
	With   struct {
		AnalyticsData bool `json:"analytics_data"`
	} `json:"with"`
}

type fboPostingListV3Resp struct {
	Cursor   string `json:"cursor"`
	HasNext  bool   `json:"has_next"`
	Postings []struct {
		PostingNumber string `json:"posting_number"`
		Status        string `json:"status"`
		AnalyticsData struct {
			WarehouseName string `json:"warehouse_name"`
			City          string `json:"city"`
		} `json:"analytics_data"`
		Products []struct {
			SKU      int64  `json:"sku"`
			OfferID  string `json:"offer_id"`
			Quantity int    `json:"quantity"`
		} `json:"products"`
	} `json:"postings"`
}

func (c *OzonClient) loadSalesVelocityByCluster(ctx context.Context, windowDays int, productsMap map[string]*RawProductData, warehouseToCluster map[string]string) error {
	if windowDays <= 0 {
		windowDays = 28
	}
	now := time.Now().UTC()
	sinceTime := now.AddDate(0, 0, -windowDays)

	since := sinceTime.Format("2006-01-02T15:04:05Z")
	to := now.Format("2006-01-02T15:04:05Z")

	cursor := ""
	limit := 100
	totalFBOOrders := 0
	mappedFBOOrders := 0

	offerIndex := buildOfferIndex(productsMap)
	skuToOffer := make(map[string]string, len(productsMap))
	for offerID, p := range productsMap {
		if p.SKU != "" {
			skuToOffer[p.SKU] = offerID
		}
	}

	for {
		var req fboPostingListV3Req
		req.Dir = "ASC"
		req.Filter.Since = since
		req.Filter.To = to
		req.Limit = limit
		req.Cursor = cursor
		req.With.AnalyticsData = true

		var resp fboPostingListV3Resp
		err := c.sendRequest(ctx, pathPostingFBOList, req, &resp)
		if err != nil {
			slog.Warn("Ошибка запроса /v3/posting/fbo/list", "err", err)
			return err
		}

		for _, post := range resp.Postings {
			if strings.EqualFold(post.Status, "cancelled") {
				continue
			}

			totalFBOOrders++
			whName := post.AnalyticsData.WarehouseName

			// Кластер берем из карты остатков: она построена на актуальных
			// данных Ozon. Если склада в ней нет, используем резервный матчер.
			cluster := warehouseToCluster[normalizeWarehouseKey(whName)]
			if cluster == "" {
				cluster = matchWarehouseClusterFallback(whName)
			}
			if cluster != "" {
				mappedFBOOrders++
			}

			for _, prod := range post.Products {
				targetOffer := strings.TrimSpace(prod.OfferID)
				if targetOffer == "" && prod.SKU != 0 {
					targetOffer = skuToOffer[strconv.FormatInt(prod.SKU, 10)]
				}

				p := lookupProduct(productsMap, offerIndex, targetOffer)
				if p == nil {
					continue
				}

				// Продажи учитываем по каждому складу отдельно, а склады кластера
				// дополнительно агрегируем в сам кластер — так же, как остатки.
				qty := float64(prod.Quantity)
				p.TotalPeriodSales += qty // общий итог без двойного счета
				if whName != "" && whName != cluster {
					p.PeriodSales[whName] += qty
				}
				if cluster != "" {
					p.PeriodSales[cluster] += qty
				}
			}
		}

		if !resp.HasNext || resp.Cursor == "" || resp.Cursor == cursor {
			break
		}
		cursor = resp.Cursor
	}

	for _, product := range productsMap {
		for cluster, periodSales := range product.PeriodSales {
			product.ClusterSales[cluster] = periodSales / float64(windowDays)
		}
	}

	slog.Info("Успешно рассчитан FBO спрос по отправлениям v3",
		"всего_fbo_заказов", totalFBOOrders,
		"с_определенным_кластером", mappedFBOOrders,
		"период_дней", windowDays)

	return nil
}

func (c *OzonClient) loadFBSStocksChunk(ctx context.Context, chunk []string, productsMap, offerIndex map[string]*RawProductData) error {
	var resp productStocksV4Resp
	err := c.sendRequest(ctx, pathProductInfoStocks, productStocksV4Req{
		Filter: struct {
			OfferID    []string `json:"offer_id,omitempty"`
			Visibility string   `json:"visibility,omitempty"`
		}{OfferID: chunk, Visibility: "ALL"},
		Limit: len(chunk),
	}, &resp)
	if err != nil {
		return err
	}

	for _, item := range resp.Items {
		prod := lookupProduct(productsMap, offerIndex, item.OfferID)
		if prod == nil {
			continue
		}
		for _, s := range item.Stocks {
			if s.Type == "fbs" {
				prod.StockFBS += s.Present
			}
		}
	}
	return nil
}

func FetchProductsFromJSON(filePath string) ([]domain.ProductSupplyRow, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file failed: %w", err)
	}

	var rows []domain.ProductSupplyRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal json failed: %w", err)
	}
	return rows, nil
}

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

// retryPause задает паузу перед повтором. В тестах она сокращается вместе с
// остальными паузами (requestOpts), чтобы тесты не ждали десятки секунд.
func (c *OzonClient) retryPause(def time.Duration) time.Duration {
	if c.requestOpts != nil && c.requestOpts.baseWait > 0 {
		return c.requestOpts.baseWait
	}
	return def
}

// DefaultReportTimeout — сколько по умолчанию ждать готовности отчета Ozon.
// Отчеты формируются асинхронно, на большом каталоге это занимает минуты.
const DefaultReportTimeout = 5 * time.Minute

// SetReportTimeout задает, сколько ждать готовности отчета Ozon.
func (c *OzonClient) SetReportTimeout(d time.Duration) {
	if d > 0 {
		c.reportTimeout = d
	}
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

// placementColumns — индексы колонок отчета о размещении. -1 означает, что
// колонки в файле нет.
type placementColumns struct {
	offer, sku, status, date, cost int
	paidQty, totalQty              int
	forecast28, forecastMonth      int
	stock, freeStock, forcedUnits  int
}

// findPlacementHeader ищет строку заголовков. Ozon отдает отчет в двух видах:
// файл API (одна строка заголовков) и выгрузка личного кабинета «Стоимость
// размещения по товарам» (две строки: группы колонок и подзаголовки).
func findPlacementHeader(rows [][]string) (start, end int) {
	for r := 0; r < len(rows) && r < 5; r++ {
		joined := normalizeHeaderCell(strings.Join(rows[r], " "))
		if !strings.Contains(joined, "артикул") && !strings.Contains(joined, "offer_id") && !strings.Contains(joined, "offer") {
			continue
		}
		start, end = r, r
		if r+1 < len(rows) && isPlacementSubHeader(rows[r+1]) {
			end = r + 1
		}
		return start, end
	}
	return -1, -1
}

// isPlacementSubHeader распознает вторую строку заголовков: в ней нет
// артикулов, зато есть названия подколонок.
func isPlacementSubHeader(row []string) bool {
	words := 0
	for _, cell := range row {
		h := normalizeHeaderCell(cell)
		if h == "" {
			continue
		}
		if containsAnyHeader(h, "списан", "остаток", "экземпляр", "денег", "прогноз", "доля", "транзит", "зарезерв", "вынужд", "просмотр", "конверсия", "дней", "индекс", "признак", "рекоменд") {
			words++
		}
	}
	return words >= 2
}

func normalizeHeaderCell(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func containsAnyHeader(h string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(h, part) {
			return true
		}
	}
	return false
}

// combinedHeader склеивает все строки заголовка по колонке: у выгрузки ЛК
// название колонки может быть в первой строке, а подзаголовок — во второй.
func combinedHeader(rows [][]string, start, end, col int) string {
	parts := make([]string, 0, 2)
	for r := start; r <= end; r++ {
		if v := cellAt(rows[r], col); v != "" {
			parts = append(parts, v)
		}
	}
	return normalizeHeaderCell(strings.Join(parts, " "))
}

// cellAt возвращает значение ячейки; col = -1 означает, что колонки нет.
func cellAt(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func detectPlacementColumns(rows [][]string, start, end int) placementColumns {
	cols := placementColumns{
		offer: -1, sku: -1, status: -1, date: -1, cost: -1,
		paidQty: -1, totalQty: -1, forecast28: -1, forecastMonth: -1,
		stock: -1, freeStock: -1, forcedUnits: -1,
	}

	maxCol := 0
	for r := start; r <= end; r++ {
		if len(rows[r]) > maxCol {
			maxCol = len(rows[r])
		}
	}

	for c := 0; c < maxCol; c++ {
		h := combinedHeader(rows, start, end, c)
		if h == "" {
			continue
		}
		switch {
		case cols.forecastMonth == -1 && strings.Contains(h, "прогноз") && strings.Contains(h, "месяц"):
			cols.forecastMonth = c
		case cols.forecast28 == -1 && strings.Contains(h, "прогноз") && strings.Contains(h, "28"):
			cols.forecast28 = c
		case cols.paidQty == -1 && strings.Contains(h, "платных"):
			cols.paidQty = c
		case cols.totalQty == -1 && containsAnyHeader(h, "экземпляр", "кол-во") && !strings.Contains(h, "платн"):
			cols.totalQty = c
		case cols.stock == -1 && strings.Contains(h, "остаток на складах") && strings.Contains(h, "шт"):
			cols.stock = c
		case cols.freeStock == -1 && strings.Contains(h, "размещение без платы") && strings.Contains(h, "шт"):
			cols.freeStock = c
		case cols.forcedUnits == -1 && strings.Contains(h, "вынужденное размещение") && strings.Contains(h, "шт"):
			cols.forcedUnits = c
		case cols.cost == -1 && containsAnyHeader(h, "списан", "стоимост", "начислен", "плата"):
			cols.cost = c
		case cols.date == -1 && isPlacementDateHeader(h):
			cols.date = c
		case cols.offer == -1 && containsAnyHeader(h, "артикул", "offer_id", "offer id", "offer"):
			cols.offer = c
		case cols.sku == -1 && containsAnyHeader(h, "sku", "product_id", "product id"):
			cols.sku = c
		case cols.status == -1 && strings.Contains(h, "статус"):
			cols.status = c
		}
	}
	return cols
}

// isPlacementDateHeader отличает дату окончания бесплатного размещения от
// «Дней до начала…» и прочих числовых колонок.
func isPlacementDateHeader(h string) bool {
	if strings.Contains(h, "дата") && containsAnyHeader(h, "бесплат", "окончан", "начал", "срок") {
		return true
	}
	return containsAnyHeader(h, "бесплатно до", "окончание бесплатного", "срок бесплатного")
}

func parseTableRows(rows [][]string) (map[string]PlacementItemData, error) {
	headerStart, headerEnd := findPlacementHeader(rows)
	if headerStart == -1 {
		return nil, fmt.Errorf("не удалось найти строку заголовков с артикулом/SKU в отчете о стоимости размещения")
	}
	cols := detectPlacementColumns(rows, headerStart, headerEnd)
	if cols.offer == -1 && cols.sku == -1 {
		return nil, fmt.Errorf("в отчете о стоимости размещения не найдены ни артикул, ни SKU")
	}

	type productAccumulator struct {
		OfferID           string
		SKU               string
		PlacementInfo     string
		PastStorageCost   float64
		FreeUntil         time.Time
		TotalPaidQty      float64
		TotalQty          float64
		OzonForecast28    float64
		OzonForecastMonth float64
		HasOzonForecast   bool
		Stock             int
		FreeStock         int
		ForcedUnits       int
	}

	accMap := make(map[string]*productAccumulator)

	for i := headerEnd + 1; i < len(rows); i++ {
		row := rows[i]
		offerID := cellAt(row, cols.offer)
		sku := cellAt(row, cols.sku)
		if offerID == "" && sku == "" {
			continue
		}
		statusVal := cellAt(row, cols.status)
		dateVal := cellAt(row, cols.date)

		key := offerID
		if key == "" {
			key = sku
		}

		acc, ok := accMap[key]
		if !ok {
			acc = &productAccumulator{
				OfferID: offerID,
				SKU:     sku,
			}
			accMap[key] = acc
		}

		acc.PastStorageCost += parseCost(cellAt(row, cols.cost))
		acc.TotalPaidQty += parseCost(cellAt(row, cols.paidQty))
		acc.TotalQty += parseCost(cellAt(row, cols.totalQty))
		acc.Stock += int(parseCost(cellAt(row, cols.stock)))
		acc.FreeStock += int(parseCost(cellAt(row, cols.freeStock)))
		acc.ForcedUnits += int(parseCost(cellAt(row, cols.forcedUnits)))

		// Прогноз Ozon есть только в выгрузке личного кабинета, в файле API
		// таких колонок нет — тогда прогноз считает программа.
		if cols.forecast28 != -1 {
			acc.OzonForecast28 += parseCost(cellAt(row, cols.forecast28))
			acc.HasOzonForecast = true
		}
		if cols.forecastMonth != -1 {
			acc.OzonForecastMonth += parseCost(cellAt(row, cols.forecastMonth))
			acc.HasOzonForecast = true
		}

		if freeUntil := parsePlacementDate(dateVal); !freeUntil.IsZero() {
			acc.FreeUntil = freeUntil
		}

		if dateVal != "" || statusVal != "" {
			acc.PlacementInfo = formatPlacementInfo(statusVal, dateVal)
		}
	}

	result := make(map[string]PlacementItemData, len(accMap)*2)
	for _, acc := range accMap {
		info := acc.PlacementInfo
		switch {
		case cols.paidQty != -1 || cols.totalQty != -1:
			if acc.TotalPaidQty > 0 || acc.PastStorageCost > 0 {
				info = "Платно"
			} else if acc.TotalQty > 0 {
				info = "Бесплатно"
			} else {
				info = "-"
			}
		case cols.stock != -1:
			// В выгрузке ЛК готового статуса нет — выводим его из данных Ozon:
			// вынужденное размещение и прогноз говорят о платном хранении.
			switch {
			case acc.Stock == 0:
				info = "-"
			case acc.ForcedUnits > 0 || acc.OzonForecast28 > 0:
				info = "Платно"
			case !acc.FreeUntil.IsZero():
				info = fmt.Sprintf("Бесплатно (до %s)", acc.FreeUntil.Format("02.01.2006"))
			default:
				info = "Бесплатно"
			}
		case info == "":
			if acc.PastStorageCost > 0 {
				info = "Платно"
			} else {
				info = "-"
			}
		}

		item := PlacementItemData{
			OfferID:           acc.OfferID,
			SKU:               acc.SKU,
			PlacementInfo:     info,
			PastStorageCost:   acc.PastStorageCost,
			FreeUntil:         acc.FreeUntil,
			OzonForecast28:    acc.OzonForecast28,
			OzonForecastMonth: acc.OzonForecastMonth,
			HasOzonForecast:   acc.HasOzonForecast,
		}

		if acc.OfferID != "" {
			result[acc.OfferID] = item
		}
		if acc.SKU != "" {
			result[acc.SKU] = item
		}
	}

	return result, nil
}

func parseCost(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "₽", "")
	s = strings.ReplaceAll(s, "руб.", "")
	s = strings.ReplaceAll(s, "руб", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")
	val, _ := strconv.ParseFloat(s, 64)
	return val
}

// placementDateLayouts — форматы даты в отчете Ozon (со временем или без него).
var placementDateLayouts = []string{"2006-01-02", "02.01.2006"}

// parsePlacementDate разбирает дату окончания бесплатного размещения. Нулевое
// время означает «даты нет» — пустая ячейка, прочерк или null.
func parsePlacementDate(raw string) time.Time {
	d := strings.TrimSpace(raw)
	if d == "" || d == "-" || d == "—" || strings.EqualFold(d, "null") {
		return time.Time{}
	}
	if idx := strings.IndexAny(d, "T "); idx != -1 {
		d = d[:idx]
	}
	for _, layout := range placementDateLayouts {
		if parsed, err := time.Parse(layout, d); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func formatPlacementInfo(statusVal, dateVal string) string {
	s := strings.ToLower(strings.TrimSpace(statusVal))
	d := strings.TrimSpace(dateVal)

	if parsedDate := parsePlacementDate(d); !parsedDate.IsZero() {
		if time.Now().After(parsedDate) {
			return "Платно"
		}
		return fmt.Sprintf("Бесплатно (до %s)", parsedDate.Format("02.01.2006"))
	}

	if d != "" && d != "-" && d != "—" && !strings.EqualFold(d, "null") {
		// Дата есть, но формат незнакомый — показываем как пришло.
		return fmt.Sprintf("Бесплатно (до %s)", d)
	}

	// Важно: "бесплатно" содержит подстроку "платно", поэтому сначала проверяем "бесплатн"!
	if strings.Contains(s, "бесплатн") || s == "да" || s == "true" || s == "free" {
		return "Бесплатно"
	}
	if strings.Contains(s, "платн") || s == "нет" || s == "false" || s == "paid" {
		return "Платно"
	}
	if s == "" {
		return "-"
	}
	return statusVal
}
