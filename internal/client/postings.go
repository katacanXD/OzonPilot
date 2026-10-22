package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxPostingPages страхует от бесконечного цикла, если Ozon начнет отдавать
// has_next без курсора и без новых записей.
const maxPostingPages = 2000

// postingPageLimit — размер страницы истории отправлений. 100 — предел метода.
const postingPageLimit = 100

// postingProduct — товар внутри отправления.
type postingProduct struct {
	SKU      int64  `json:"sku"`
	OfferID  string `json:"offer_id"`
	Quantity int    `json:"quantity"`
}

// postingClusterData — кластеры, которые Ozon отдает в аналитических и
// финансовых данных отправления. cluster_to — это кластер, куда уехал товар;
// именно он соответствует разрезу «Кластер отгрузки → Товары» в кабинете.
type postingClusterData struct {
	WarehouseName string `json:"warehouse_name"`
	City          string `json:"city"`
	ClusterFrom   string `json:"cluster_from"`
	ClusterTo     string `json:"cluster_to"`
}

type postingItem struct {
	PostingNumber string             `json:"posting_number"`
	Status        string             `json:"status"`
	CreatedAt     string             `json:"created_at"`
	InProcessAt   string             `json:"in_process_at"`
	AnalyticsData postingClusterData `json:"analytics_data"`
	FinancialData postingClusterData `json:"financial_data"`
	Products      []postingProduct   `json:"products"`
}

// postingListReq — запрос списка отправлений. Старые версии метода листают по
// offset, новые — по cursor; отправляем оба параметра. Лишний параметр Ozon
// игнорирует, поэтому пагинация работает на любой версии метода.
type postingListReq struct {
	Dir    string `json:"dir"`
	Filter struct {
		Since string `json:"since"`
		To    string `json:"to"`
	} `json:"filter"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor,omitempty"`
	Offset int    `json:"offset,omitempty"`
	With   struct {
		AnalyticsData bool `json:"analytics_data"`
		FinancialData bool `json:"financial_data"`
	} `json:"with"`
}

// postingListResp поддерживает оба известных формата ответа: плоский
// (postings в корне) и обернутый в result.
type postingListResp struct {
	Cursor   string        `json:"cursor"`
	HasNext  bool          `json:"has_next"`
	Postings []postingItem `json:"postings"`
	Result   struct {
		Cursor   string        `json:"cursor"`
		HasNext  bool          `json:"has_next"`
		Postings []postingItem `json:"postings"`
	} `json:"result"`
}

// page возвращает страницу отправлений независимо от формата ответа.
func (r *postingListResp) page() (items []postingItem, hasNext bool, cursor string) {
	if len(r.Postings) > 0 {
		return r.Postings, r.HasNext, r.Cursor
	}
	if len(r.Result.Postings) > 0 {
		return r.Result.Postings, r.Result.HasNext, r.Result.Cursor
	}
	// Пустая страница: has_next сообщаем вызывающему, чтобы он остановился
	// осознанно, а не считал данные полными по умолчанию.
	if r.Cursor != "" {
		return nil, r.HasNext, r.Cursor
	}
	return nil, r.Result.HasNext, r.Result.Cursor
}

// postingQuery описывает выборку истории отправлений одного метода.
type postingQuery struct {
	path          string
	since, to     time.Time
	withFinancial bool
}

// errPostingPageStalled — Ozon вернул has_next без нового курсора и без строк.
// Такой ответ нельзя считать признаком конца истории, поэтому о нем сообщают
// вызывающему: collectSales пишет предупреждение, диагностика — молча
// останавливается.
var errPostingPageStalled = errors.New("ozon вернул has_next без курсора и без записей")

// forEachPostingPage листает историю отправлений и отдает каждую страницу
// обработчику. Различия методов скрыты здесь: Ozon нумерует страницы курсором
// или offset, а ответ приходит в двух форматах (/v3 оборачивает в result,
// /v4 кладет postings в корень).
func (c *OzonClient) forEachPostingPage(ctx context.Context, q postingQuery, fn func(items []postingItem, page int) error) error {
	cursor, offset := "", 0
	for page := 1; page <= maxPostingPages; page++ {
		var resp postingListResp
		if err := c.sendRequest(ctx, q.path, newPostingListReq(q, cursor, offset), &resp); err != nil {
			return err
		}

		items, hasNext, nextCursor := resp.page()
		if err := fn(items, page); err != nil {
			return err
		}

		if !hasNext {
			return nil
		}
		// Новые версии метода листают по курсору, старые — по offset.
		if nextCursor != "" && nextCursor != cursor {
			cursor = nextCursor
			continue
		}
		if len(items) == 0 {
			return errPostingPageStalled
		}
		offset += len(items)
	}
	return nil
}

// newPostingListReq собирает запрос страницы: даты в формате Ozon, оба способа
// пагинации, аналитические данные всегда и финансовые — по требованию.
func newPostingListReq(q postingQuery, cursor string, offset int) postingListReq {
	var req postingListReq
	req.Dir = "ASC"
	req.Filter.Since = q.since.Format("2006-01-02T15:04:05Z")
	req.Filter.To = q.to.Format("2006-01-02T15:04:05Z")
	req.Limit = postingPageLimit
	req.Cursor = cursor
	req.Offset = offset
	req.With.AnalyticsData = true
	req.With.FinancialData = q.withFinancial
	return req
}

// salesStats — что удалось собрать из списка отправлений. Нужен для лога:
// по нему видно, сколько заказов попало в кластеры, а сколько осталось только
// в общем итоге.
type salesStats struct {
	scheme        string
	postings      int
	units         float64
	unmapped      int
	unmappedUnits float64
	cancelled     int
	warehouses    map[string]bool
	byCluster     map[string]float64
}

// resolveSalesCluster определяет кластер заказа: сначала кластер назначения
// (cluster_to — «куда уехал товар»), затем кластер отгрузки, затем карта
// «склад → кластер» из остатков и резервный матчер по названию склада.
func resolveSalesCluster(post postingItem, warehouseToCluster map[string]string) (cluster, warehouse string) {
	warehouse = post.AnalyticsData.WarehouseName

	for _, raw := range []string{
		post.FinancialData.ClusterTo,
		post.AnalyticsData.ClusterTo,
		post.FinancialData.ClusterFrom,
		post.AnalyticsData.ClusterFrom,
	} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if name := NormalizeClusterName(raw); name != "" {
			return name, warehouse
		}
	}

	if warehouse == "" {
		return "", ""
	}
	if cluster := warehouseToCluster[normalizeWarehouseKey(warehouse)]; cluster != "" {
		return cluster, warehouse
	}
	return matchWarehouseClusterFallback(warehouse), warehouse
}

// lookupPostingProduct находит товар отправления по артикулу, а при пустом
// артикуле — по SKU.
func lookupPostingProduct(productsMap, offerIndex map[string]*RawProductData, skuToOffer map[string]string, prod postingProduct) *RawProductData {
	targetOffer := strings.TrimSpace(prod.OfferID)
	if targetOffer == "" && prod.SKU != 0 {
		targetOffer = skuToOffer[strconv.FormatInt(prod.SKU, 10)]
	}
	return lookupProduct(productsMap, offerIndex, targetOffer)
}

// loadSalesVelocityByCluster собирает заказы за окно по обеим схемам: FBO и
// FBS. Кластер заказа — тот, куда уехал товар (cluster_to), поэтому разрез
// совпадает с аналитикой кабинета «Кластер отгрузки → Товары».
func (c *OzonClient) loadSalesVelocityByCluster(ctx context.Context, windowDays int, productsMap map[string]*RawProductData, warehouseToCluster map[string]string) error {
	if windowDays <= 0 {
		windowDays = 28
	}

	// FBO — основа истории: без нее расчет поставок бессмыслен, ошибка критична.
	fbo, err := c.collectSalesWithFallback(ctx, pathPostingFBOList, "FBO", windowDays, productsMap, warehouseToCluster)
	if err != nil {
		slog.Warn("Ошибка запроса списка отправлений FBO", "err", err)
		return err
	}

	// FBS — вторая половина заказов: товар может продаваться только с
	// собственного склада. Сбой FBS не должен ронять весь расчет.
	fbs, fbsErr := c.collectSalesWithFallback(ctx, pathPostingFBSList, "FBS", windowDays, productsMap, warehouseToCluster)
	if fbsErr != nil {
		slog.Warn("Не удалось получить отправления FBS через /v4 — пробую /v3", "err", fbsErr)
		fbs, fbsErr = c.collectSalesWithFallback(ctx, pathPostingFBSListLegacy, "FBS", windowDays, productsMap, warehouseToCluster)
	}
	if fbsErr != nil {
		slog.Warn("Не удалось получить отправления FBS — заказы посчитаны только по FBO", "err", fbsErr)
	}

	for _, product := range productsMap {
		for cluster, periodSales := range product.PeriodSales {
			product.ClusterSales[cluster] = periodSales / float64(windowDays)
		}
	}

	logSalesStats(fbo)
	if fbsErr == nil {
		logSalesStats(fbs)
	}

	return nil
}

// collectSalesWithFallback повторяет запрос без financial_data, если метод его
// не принял: тогда кластер берется из аналитических данных отправления или из
// карты остатков. Повтор безопасен — заказы применяются только после успешной
// загрузки всех страниц.
func (c *OzonClient) collectSalesWithFallback(ctx context.Context, path, scheme string, windowDays int, productsMap map[string]*RawProductData, warehouseToCluster map[string]string) (salesStats, error) {
	stats, err := c.collectSales(ctx, path, scheme, windowDays, productsMap, warehouseToCluster, true)
	if err == nil {
		return stats, nil
	}
	// 404 и 5xx означают «метода нет / сервис недоступен» — повтор без
	// financial_data тут не поможет. 400 — похоже на неподдерживаемый параметр.
	if !hasStatus(err, http.StatusBadRequest) {
		return stats, err
	}
	slog.Warn("Метод отправлений не принял financial_data — повтор без него", "схема", scheme, "метод", path, "err", err)
	return c.collectSales(ctx, path, scheme, windowDays, productsMap, warehouseToCluster, false)
}

// salesEntry — отложенное начисление заказа: применяется, только когда список
// отправлений загружен целиком.
type salesEntry struct {
	product *RawProductData
	cluster string
	qty     float64
}

// collectSales листает список отправлений одной схемы и раскладывает заказы
// по кластерам назначения.
func (c *OzonClient) collectSales(ctx context.Context, path, scheme string, windowDays int, productsMap map[string]*RawProductData, warehouseToCluster map[string]string, financialData bool) (salesStats, error) {
	stats := salesStats{
		scheme:     scheme,
		warehouses: make(map[string]bool),
		byCluster:  make(map[string]float64),
	}
	var entries []salesEntry

	// Окно считается по календарным дням, как в кабинете: 28 дней — это сегодня
	// плюс 27 предыдущих, а не 28 суток назад.
	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	since := dayStart.AddDate(0, 0, -(windowDays - 1))

	offerIndex := buildOfferIndex(productsMap)
	skuToOffer := make(map[string]string, len(productsMap))
	for offerID, p := range productsMap {
		if p.SKU != "" {
			skuToOffer[p.SKU] = offerID
		}
	}

	err := c.forEachPostingPage(ctx, postingQuery{
		path:          path,
		since:         since,
		to:            now,
		withFinancial: financialData,
	}, func(items []postingItem, page int) error {
		// В -debug показываем одно реальное отправление: по нему видно, какие
		// кластеры и склад Ozon вернул на самом деле.
		if page == 1 && len(items) > 0 && slog.Default().Enabled(ctx, slog.LevelDebug) {
			if raw, err := json.Marshal(items[0]); err == nil {
				slog.Debug("Пример отправления Ozon", "схема", scheme, "json", string(raw))
			}
		}

		for _, post := range items {
			// Отменённые заказы тоже считаем: показатель кабинета «Заказано
			// товаров» их включает, иначе цифры расходятся на отменах.
			if strings.EqualFold(post.Status, "cancelled") {
				stats.cancelled++
			}

			stats.postings++
			cluster, whName := resolveSalesCluster(post, warehouseToCluster)
			if cluster == "" {
				stats.unmapped++
				if whName != "" {
					stats.warehouses[whName] = true
				}
			}

			for _, prod := range post.Products {
				p := lookupPostingProduct(productsMap, offerIndex, skuToOffer, prod)
				if p == nil {
					continue
				}

				qty := float64(prod.Quantity)
				stats.units += qty
				if cluster == "" {
					stats.unmappedUnits += qty
				}
				entries = append(entries, salesEntry{product: p, cluster: cluster, qty: qty})
			}
		}
		return nil
	})
	if errors.Is(err, errPostingPageStalled) {
		slog.Warn("Ozon вернул has_next без курсора и без записей — история отправлений может быть неполной",
			"схема", scheme)
	} else if err != nil {
		return stats, err
	}

	// Заказы применяются только теперь: если запрос сорвется на второй странице,
	// повтор без financial_data не удвоит уже собранные штуки.
	for _, entry := range entries {
		entry.product.TotalPeriodSales += entry.qty // общий итог без двойного счета
		if entry.cluster == "" {
			continue
		}
		entry.product.PeriodSales[entry.cluster] += entry.qty
		c.salesClusters[entry.cluster] = true
		stats.byCluster[entry.cluster] += entry.qty
	}

	return stats, nil
}

// logSalesStats пишет в лог, сколько заказов разложено по кластерам, а сколько
// осталось только в общем итоге: это и есть источник расхождений с кабинетом.
func logSalesStats(s salesStats) {
	if s.unmapped > 0 {
		sample := make([]string, 0, 5)
		for wh := range s.warehouses {
			if len(sample) == 5 {
				break
			}
			sample = append(sample, wh)
		}
		slog.Warn("Часть отправлений не удалось отнести к кластеру — они учтены только в общем итоге",
			"схема", s.scheme,
			"отправлений_без_кластера", s.unmapped,
			"штук_без_кластера", s.unmappedUnits,
			"примеры_складов", strings.Join(sample, ", "))
	}
	tally := make([]string, 0, len(s.byCluster))
	for name, qty := range s.byCluster {
		tally = append(tally, fmt.Sprintf("%s=%.0f", name, qty))
	}
	sort.Strings(tally)

	slog.Info("Заказы загружены",
		"схема", s.scheme,
		"отправлений", s.postings,
		"штук", s.units,
		"из_них_отменённых", s.cancelled,
		"без_кластера", s.unmapped,
		"по_кластерам", strings.Join(tally, ", "))
}
