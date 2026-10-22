package client

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

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
			err := c.sendRequest(ctx, pathAnalyticsStocks, analyticsStocksReq{
				SKUs: chunk,
			}, &resp)
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

// checkFBOCoverage убеждается, что остатки пришли по всем товарам, у которых
// есть SKU: иначе часть позиций молча получила бы нулевой остаток.
func checkFBOCoverage(productsMap map[string]*RawProductData, coveredSKUs map[string]bool) error {
	var uncovered []string
	for offerID, prod := range productsMap {
		if prod.SKU != "" && !coveredSKUs[prod.SKU] {
			uncovered = append(uncovered, offerID)
		}
	}
	if len(uncovered) == 0 {
		return nil
	}

	sample := uncovered
	if len(sample) > 5 {
		sample = sample[:5]
	}
	return fmt.Errorf(
		"остатки FBO не получены для %d товаров (например: %s) — расчет остановлен во избежание неверных поставок",
		len(uncovered), strings.Join(sample, ", "))
}
