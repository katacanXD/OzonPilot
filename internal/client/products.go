package client

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// RawProductData — товар, собранный из нескольких методов Ozon: каталога,
// остатков FBO и истории отправлений.
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

// FetchAllProducts собирает по каждому товару остатки FBO/FBS и историю продаж.
// Загрузка идет конвейером: каталог → SKU и названия → остатки FBO → остатки
// FBS → продажи. Каждый этап нужен следующему, поэтому неполные данные
// прерывают расчет, а не превращаются в нули в отчёте.
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
	if err := checkFBOCoverage(productsMap, coveredSKUs); err != nil {
		return nil, err
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

// loadFBSStocksChunk загружает остатки собственного склада продавца (FBS)
// пачкой до 100 артикулов через /v4/product/info/stocks.
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
