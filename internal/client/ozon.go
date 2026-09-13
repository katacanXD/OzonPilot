package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"CLIForOZON/internal/domain"
)

type OzonClient struct {
	clientID   string
	apiKey     string
	httpClient *http.Client
}

func NewOzonClient(clientID, apiKey string) *OzonClient {
	return &OzonClient{
		clientID: clientID,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: 35 * time.Second,
		},
	}
}

func (c *OzonClient) sendRequest(ctx context.Context, endpoint string, reqPayload, respTarget any) error {
	var bodyBytes []byte
	var err error
	if reqPayload != nil {
		bodyBytes, err = json.Marshal(reqPayload)
		if err != nil {
			return fmt.Errorf("marshal request failed: %w", err)
		}
	}

	maxRetries := 4

	for attempt := 0; attempt <= maxRetries; attempt++ {
		time.Sleep(500 * time.Millisecond)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("create request failed: %w", err)
		}

		req.Header.Set("Client-Id", c.clientID)
		req.Header.Set("Api-Key", c.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("http request error: %w", err)
		}

		respBytes, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response body failed: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt < maxRetries {
				wait := time.Duration(attempt+2) * 1500 * time.Millisecond
				slog.Warn("Лимит запросов Ozon (429). Ждем сброса лимита...", "wait", wait)
				time.Sleep(wait)
				continue
			}
			return fmt.Errorf("ozon api rate limit exceeded (429): %s", string(respBytes))
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("ozon api error (status %d): %s", resp.StatusCode, string(respBytes))
		}

		if respTarget != nil {
			if err := json.Unmarshal(respBytes, respTarget); err != nil {
				return fmt.Errorf("unmarshal response error: %w", err)
			}
		}
		return nil
	}

	return fmt.Errorf("max retries exceeded for %s", endpoint)
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

type stockOnWarehousesReq struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type stockOnWarehousesResp struct {
	Result struct {
		Rows []struct {
			SKU             int64  `json:"sku"`
			ItemCode        string `json:"item_code"`
			ItemName        string `json:"item_name"`
			Cluster         string `json:"cluster"`
			FreeToSellStock int    `json:"free_to_sell_stock"`
		} `json:"rows"`
	} `json:"result"`
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

type analyticsFilter struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type analyticsDataReq struct {
	DateFrom  string            `json:"date_from"`
	DateTo    string            `json:"date_to"`
	Metrics   []string          `json:"metrics"`
	Dimension []string          `json:"dimension"`
	Filters   []analyticsFilter `json:"filters,omitempty"`
	Limit     int               `json:"limit"`
	Offset    int               `json:"offset"`
}

type analyticsDataResp struct {
	Result struct {
		Data []struct {
			Dimensions []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"dimensions"`
			Metrics []float64 `json:"metrics"`
		} `json:"data"`
	} `json:"result"`
}

type RawProductData struct {
	OfferID      string
	SKU          string
	Name         string
	StockFBS     int
	FBOClusters  map[string]int     // cluster -> stock
	ClusterSales map[string]float64 // cluster -> daily sales
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

		err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v3/product/list", req, &listResp)
		if err != nil {
			return nil, fmt.Errorf("fetch products batch failed: %w", err)
		}

		for _, item := range listResp.Result.Items {
			offerIDs = append(offerIDs, item.OfferID)
			productsMap[item.OfferID] = &RawProductData{
				OfferID:      item.OfferID,
				FBOClusters:  make(map[string]int),
				ClusterSales: make(map[string]float64),
			}
		}

		if len(listResp.Result.Items) == 0 || listResp.Result.LastID == "" || listResp.Result.LastID == lastID {
			break
		}
		lastID = listResp.Result.LastID
	}

	slog.Info("Всего товаров получено", "count", len(productsMap))

	slog.Info("Загрузка FBO остатков по кластерам...")
	if err := c.loadFBOStocks(ctx, productsMap); err != nil {
		slog.Warn("Не удалось получить FBO остатки", "err", err)
	}

	slog.Info("Загрузка FBS остатков...")
	chunkSize := 100
	for i := 0; i < len(offerIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(offerIDs) {
			end = len(offerIDs)
		}
		if err := c.loadFBSStocksChunk(ctx, offerIDs[i:end], productsMap); err != nil {
			slog.Warn("Не удалось получить FBS остатки для пакета", "from", i, "to", end, "err", err)
		}
	}

	slog.Info("Загрузка аналитики продаж по кластерам...", "window_days", windowDays)
	if err := c.loadSalesVelocityByCluster(ctx, windowDays, productsMap); err != nil {
		slog.Warn("Не удалось получить аналитику продаж по кластерам", "err", err)
	}

	return productsMap, nil
}

// Определяет, относится ли склад Ozon к московскому региону / кластеру
// isMoscowWarehouse проверяет, относится ли склад к кластеру "Москва, МО и Дальние регионы"
func isMoscowWarehouse(name string) bool {
	n := strings.ToLower(name)

	// Полный список локаций складов Московского кластера Ozon
	moscowKeywords := []string{
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

	for _, kw := range moscowKeywords {
		if strings.Contains(n, kw) {
			return true
		}
	}
	return false
}

func (c *OzonClient) loadFBOStocks(ctx context.Context, productsMap map[string]*RawProductData) error {
	offset := 0
	limit := 1000

	type fboItem struct {
		ItemCode         string `json:"item_code"`
		SKU              int64  `json:"sku"`
		ItemName         string `json:"item_name"`
		WarehouseName    string `json:"warehouse_name"`
		FreeToSellAmount int    `json:"free_to_sell_amount"` // Ключевое исправленное поле!
	}

	for {
		var resp struct {
			Result struct {
				Rows []fboItem `json:"rows"`
			} `json:"result"`
		}

		err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v2/analytics/stock_on_warehouses", stockOnWarehousesReq{
			Limit:  limit,
			Offset: offset,
		}, &resp)
		if err != nil {
			return err
		}

		for _, row := range resp.Result.Rows {
			itemCode := strings.TrimSpace(row.ItemCode)
			prod, ok := productsMap[itemCode]
			if !ok {
				for k, v := range productsMap {
					if strings.EqualFold(k, itemCode) {
						prod = v
						ok = true
						break
					}
				}
			}
			if !ok {
				continue
			}

			if prod.SKU == "" && row.SKU != 0 {
				prod.SKU = strconv.FormatInt(row.SKU, 10)
			}
			if prod.Name == "" || prod.Name == prod.OfferID {
				prod.Name = row.ItemName
			}

			// Если склад относится к Москве — заносим в кластер "Москва"
			if isMoscowWarehouse(row.WarehouseName) {
				prod.FBOClusters["Москва"] += row.FreeToSellAmount
			}

			// Также сохраняем по точному имени склада
			prod.FBOClusters[row.WarehouseName] += row.FreeToSellAmount
		}

		if len(resp.Result.Rows) < limit {
			break
		}
		offset += limit
	}

	return nil
}

type fboPostingListReq struct {
	Dir    string           `json:"dir"`
	Filter fboPostingFilter `json:"filter"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
	With   fboPostingWith   `json:"with"`
}

type fboPostingFilter struct {
	Since  string `json:"since"`
	To     string `json:"to"`
	Status string `json:"status,omitempty"`
}

type fboPostingWith struct {
	AnalyticsData bool `json:"analytics_data"`
}

type fboPostingListResp struct {
	Result []struct {
		PostingNumber string `json:"posting_number"`
		AnalyticsData struct {
			ClusterFrom string `json:"cluster_from"` // Откуда уехал заказ
			ClusterTo   string `json:"cluster_to"`   // Куда поехал заказ
		} `json:"analytics_data"`
		Products []struct {
			SKU      int64  `json:"sku"`
			OfferID  string `json:"offer_id"`
			Quantity int    `json:"quantity"`
		} `json:"products"`
	} `json:"result"`
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

func (c *OzonClient) loadSalesVelocityByCluster(ctx context.Context, windowDays int, productsMap map[string]*RawProductData) error {
	now := time.Now().UTC()
	sinceTime := now.AddDate(0, 0, -windowDays)

	since := sinceTime.Format("2006-01-02T15:04:05Z")
	to := now.Format("2006-01-02T15:04:05Z")

	cursor := ""
	limit := 100 // В v3 с курсором можно брать сразу по 1000 штук за раз!
	totalFBOOrders := 0
	moscowFBOOrders := 0

	skuToOffer := make(map[string]string)
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
		err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v3/posting/fbo/list", req, &resp)
		if err != nil {
			slog.Warn("Ошибка запроса /v3/posting/fbo/list", "err", err)
			return err
		}

		for _, post := range resp.Postings {
			// Игнорируем отмененные заказы
			if strings.ToLower(post.Status) == "cancelled" {
				continue
			}

			totalFBOOrders++
			whName := post.AnalyticsData.WarehouseName

			// Проверяем, что отгрузка была со складов Москвы/МО (Пушкино, Жуковский, Софьино, Хоругвино, Домодедово и др.)
			if !isMoscowWarehouse(whName) {
				continue
			}
			moscowFBOOrders++

			for _, prod := range post.Products {
				targetOffer := strings.TrimSpace(prod.OfferID)
				if targetOffer == "" && prod.SKU != 0 {
					targetOffer = skuToOffer[strconv.FormatInt(prod.SKU, 10)]
				}

				if p, ok := productsMap[targetOffer]; ok {
					p.ClusterSales["Москва"] += float64(prod.Quantity) / float64(windowDays)
				}
			}
		}

		// Пагинация по курсору Ozon
		if !resp.HasNext || resp.Cursor == "" || resp.Cursor == cursor {
			break
		}
		cursor = resp.Cursor
		time.Sleep(300 * time.Millisecond)
	}

	slog.Info("Успешно рассчитан FBO спрос по отправлениям v3",
		"всего_fbo_заказов", totalFBOOrders,
		"московских_fbo", moscowFBOOrders,
		"период_дней", windowDays)

	return nil
}

// Запасной сбор без фильтра (если у кабинета нет поддержки фильтра delivery_schema)
func (c *OzonClient) loadSalesVelocityTotal(ctx context.Context, windowDays int, productsMap map[string]*RawProductData, skuToOffer map[string]string) error {
	now := time.Now()
	from := now.AddDate(0, 0, -windowDays).Format("2006-01-02")
	to := now.Format("2006-01-02")

	var resp struct {
		Result struct {
			Data []struct {
				Dimensions []struct {
					ID string `json:"id"`
				} `json:"dimensions"`
				Metrics []float64 `json:"metrics"`
			} `json:"data"`
		} `json:"result"`
	}

	err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v1/analytics/data", analyticsDataReq{
		DateFrom:  from,
		DateTo:    to,
		Metrics:   []string{"ordered_units"},
		Dimension: []string{"sku"},
		Limit:     1000,
		Offset:    0,
	}, &resp)
	if err != nil {
		return err
	}

	for _, row := range resp.Result.Data {
		if len(row.Dimensions) == 0 || len(row.Metrics) == 0 {
			continue
		}
		sku := row.Dimensions[0].ID
		orderedUnits := row.Metrics[0]
		// Если фильтр не сработал, берём долю Москвы в 40% от общего объема
		daily := (orderedUnits * 0.4) / float64(windowDays)

		if offerID, ok := skuToOffer[sku]; ok {
			productsMap[offerID].ClusterSales["Москва"] = daily
		}
	}
	return nil
}

// Запасной вариант, если Ozon отклонит dimension delivery_schema
func (c *OzonClient) fallbackSalesVelocity(ctx context.Context, windowDays int, productsMap map[string]*RawProductData) error {
	now := time.Now()
	from := now.AddDate(0, 0, -windowDays).Format("2006-01-02")
	to := now.Format("2006-01-02")

	skuToOffer := make(map[string]string)
	for offerID, p := range productsMap {
		if p.SKU != "" {
			skuToOffer[p.SKU] = offerID
		}
	}

	var resp struct {
		Result struct {
			Data []struct {
				Dimensions []struct {
					ID string `json:"id"`
				} `json:"dimensions"`
				Metrics []float64 `json:"metrics"`
			} `json:"data"`
		} `json:"result"`
	}

	err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v1/analytics/data", analyticsDataReq{
		DateFrom:  from,
		DateTo:    to,
		Metrics:   []string{"ordered_units"},
		Dimension: []string{"sku"},
		Limit:     1000,
		Offset:    0,
	}, &resp)
	if err != nil {
		return err
	}

	for _, row := range resp.Result.Data {
		if len(row.Dimensions) == 0 || len(row.Metrics) == 0 {
			continue
		}
		sku := row.Dimensions[0].ID
		orderedUnits := row.Metrics[0]

		// Москва и Центральный регион обычно генерируют ~45-50% общероссийского спроса Ozon
		moscowShare := 0.45
		if offerID, ok := skuToOffer[sku]; ok {
			productsMap[offerID].ClusterSales["Москва"] = (orderedUnits * moscowShare) / float64(windowDays)
		}
	}
	return nil
}

func (c *OzonClient) loadFBSStocksChunk(ctx context.Context, chunk []string, productsMap map[string]*RawProductData) error {
	var resp productStocksV4Resp
	err := c.sendRequest(ctx, "https://api-seller.ozon.ru/v4/product/info/stocks", productStocksV4Req{
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
		prod, ok := productsMap[item.OfferID]
		if !ok {
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
