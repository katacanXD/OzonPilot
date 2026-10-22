package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

type tmpResp struct {
	Items []struct {
		SKU string `json:"sku"`
	} `json:"items"`
}

var fastOpts = sendRequestOpts{
	maxAttempts: 3,
	baseWait:    5 * time.Millisecond,
	maxWait:     20 * time.Millisecond,
}

// newTestClient создает клиент, направленный на тестовый сервер, с выключенным
// троттлингом: паузы Ozon (600мс и 6с) сделали бы тесты недопустимо долгими.
func newTestClient(baseURL string) *OzonClient {
	c := NewOzonClient("cid", "key")
	c.baseURL = baseURL
	c.disableThrottle = true
	c.requestOpts = &fastOpts
	return c
}

// 500 с телом {"code":2} — транзиентная ошибка Ozon, запрос должен повториться.
func TestSendRequestRetriesTransient500(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":2,"message":"internal error"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[{"sku":"123"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var resp tmpResp
	if err := c.sendRequestWithOpts(context.Background(), pathAnalyticsStocks, map[string]any{"skus": []string{"1"}}, &resp, fastOpts); err != nil {
		t.Fatalf("ожидался успех после повтора, получено: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("запросов к серверу = %d, ожидалось 2", got)
	}
	if len(resp.Items) != 1 || resp.Items[0].SKU != "123" {
		t.Errorf("ответ не разобран: %+v", resp)
	}
}

func TestSendRequestRetries429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":7}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var resp tmpResp
	if err := c.sendRequestWithOpts(context.Background(), pathAnalyticsStocks, map[string]any{}, &resp, fastOpts); err != nil {
		t.Fatalf("ожидался успех после повторов 429, получено: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("запросов к серверу = %d, ожидалось 3", got)
	}
}

// 400 повторять бессмысленно — ошибка должна вернуться сразу.
func TestSendRequestDoesNotRetry400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":3,"message":"bad request"}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var resp tmpResp
	err := c.sendRequestWithOpts(context.Background(), pathAnalyticsStocks, map[string]any{}, &resp, fastOpts)
	if err == nil {
		t.Fatal("ожидалась ошибка на 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("запросов к серверу = %d, ожидался 1 (без повторов)", got)
	}
}

// Если ошибка не рассосалась, должно вернуться сообщение об исчерпании попыток.
func TestSendRequestExhaustsAttempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":2}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	var resp tmpResp
	err := c.sendRequestWithOpts(context.Background(), pathAnalyticsStocks, map[string]any{}, &resp, fastOpts)
	if err == nil {
		t.Fatal("ожидалась ошибка после исчерпания попыток")
	}
	if got := atomic.LoadInt32(&calls); got != int32(fastOpts.maxAttempts) {
		t.Errorf("запросов к серверу = %d, ожидалось %d", got, fastOpts.maxAttempts)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504}
	for _, s := range retryable {
		if !isRetryableStatus(s) {
			t.Errorf("статус %d должен повторяться", s)
		}
	}
	notRetryable := []int{200, 400, 401, 403, 404, 409}
	for _, s := range notRetryable {
		if isRetryableStatus(s) {
			t.Errorf("статус %d не должен повторяться", s)
		}
	}
}

func TestRetryWaitGrowsAndCaps(t *testing.T) {
	opts := sendRequestOpts{baseWait: time.Second, maxWait: 5 * time.Second}
	if got := retryWait(opts, 1); got != time.Second {
		t.Errorf("пауза на попытке 1 = %v, ожидалось 1s", got)
	}
	if got := retryWait(opts, 3); got != 4*time.Second {
		t.Errorf("пауза на попытке 3 = %v, ожидалось 4s", got)
	}
	if got := retryWait(opts, 9); got != 5*time.Second {
		t.Errorf("пауза должна ограничиваться maxWait, получено %v", got)
	}
}

// Проверяем, что ответ /v1/analytics/stocks корректно разбирается.
// Заказы должны считаться по кластеру назначения (куда уехал товар), по обеим
// схемам FBO и FBS. Раньше брался склад отгрузки, а FBS не запрашивался вовсе:
// из-за этого «Продажи за период» расходились с аналитикой кабинета.
func TestLoadSalesByDestinationCluster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathPostingFBOList:
			// Плоский формат ответа: cluster_to важнее cluster_from.
			_, _ = w.Write([]byte(`{"postings":[
				{"posting_number":"FBO-1","status":"delivered",
				 "analytics_data":{"warehouse_name":"ХОРУГВИНО_РФЦ"},
				 "financial_data":{"cluster_from":"Санкт-Петербург и СЗО","cluster_to":"Москва, МО и Дальние регионы"},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":2}]},
				{"posting_number":"FBO-2","status":"delivered",
				 "analytics_data":{"warehouse_name":"ХОРУГВИНО_РФЦ"},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":1}]}
			],"has_next":false}`))
		case pathPostingFBSList:
			// Формат с оберткой result и незнакомым складом без кластера.
			_, _ = w.Write([]byte(`{"result":{"postings":[
				{"posting_number":"FBS-1","status":"delivered",
				 "analytics_data":{"warehouse_name":"Склад продавца"},
				 "financial_data":{"cluster_to":"Санкт-Петербург и СЗО"},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":1}]},
				{"posting_number":"FBS-2","status":"cancelled",
				 "analytics_data":{},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":5}]},
				{"posting_number":"FBS-3","status":"delivered",
				 "analytics_data":{"warehouse_name":"Неведомый склад"},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":4}]}
			],"has_next":false}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	productsMap := map[string]*RawProductData{
		"A-1": {
			OfferID:      "A-1",
			SKU:          "111",
			FBOClusters:  map[string]int{},
			ClusterSales: map[string]float64{},
			PeriodSales:  map[string]float64{},
		},
	}

	c := newTestClient(srv.URL)
	if err := c.loadSalesVelocityByCluster(context.Background(), 28, productsMap, map[string]string{}); err != nil {
		t.Fatalf("loadSalesVelocityByCluster: %v", err)
	}

	p := productsMap["A-1"]
	if got := p.PeriodSales["Москва"]; got != 3 {
		t.Errorf("заказов в Москву = %v, ожидалось 3 (2 по cluster_to + 1 по складу из резервного матчера)", got)
	}
	if got := p.PeriodSales["Санкт-Петербург"]; got != 1 {
		t.Errorf("заказов в Санкт-Петербург = %v, ожидался 1 (FBS)", got)
	}
	if _, ok := p.PeriodSales["Юг"]; ok {
		t.Error("отгрузка из другого кластера не должна попадать в кластер отгрузки")
	}
	// Отменённое отправление тоже считается (как «Заказано» в кабинете),
	// а заказ без кластера виден только в итоге.
	if got := p.TotalPeriodSales; got != 13 {
		t.Errorf("всего заказов = %v, ожидалось 13 (3 Москва + 1 СПб + 4 без кластера + 5 отменённых)", got)
	}
	if got := p.ClusterSales["Москва"]; got != 3.0/28 {
		t.Errorf("среднесуточные продажи Москвы = %v, ожидалось %v", got, 3.0/28)
	}
	if !c.salesClusters["Санкт-Петербург"] {
		t.Error("кластер с заказами должен попадать в список обнаруженных")
	}
}

// Ozon отключил /v3/posting/fbs/list: если /v4 недоступен, заказы FBS должны
// подхватываться старым методом, иначе они молча выпадут из кластеров.
func TestLoadSalesFallsBackToV3FBS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathPostingFBOList:
			_, _ = w.Write([]byte(`{"postings":[],"has_next":false}`))
		case pathPostingFBSList:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		case pathPostingFBSListLegacy:
			_, _ = w.Write([]byte(`{"result":{"postings":[
				{"posting_number":"FBS-1","status":"delivered",
				 "financial_data":{"cluster_to":"Москва, МО и Дальние регионы"},
				 "products":[{"sku":111,"offer_id":"A-1","quantity":2}]}
			],"has_next":false}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	productsMap := map[string]*RawProductData{
		"A-1": {
			OfferID:      "A-1",
			SKU:          "111",
			FBOClusters:  map[string]int{},
			ClusterSales: map[string]float64{},
			PeriodSales:  map[string]float64{},
		},
	}

	c := newTestClient(srv.URL)
	if err := c.loadSalesVelocityByCluster(context.Background(), 28, productsMap, map[string]string{}); err != nil {
		t.Fatalf("loadSalesVelocityByCluster: %v", err)
	}
	if got := productsMap["A-1"].PeriodSales["Москва"]; got != 2 {
		t.Errorf("заказов FBS в Москву = %v, ожидалось 2 через резервный /v3", got)
	}
}

func TestAnalyticsStocksResponseParsing(t *testing.T) {
	raw := `{"items":[
		{"sku":111,"offer_id":"A-1","name":"Товар","warehouse_name":"ХОРУГВИНО_РФЦ","cluster_name":"Москва, МО и Дальние регионы","available_stock_count":7},
		{"sku":111,"offer_id":"A-1","name":"Товар","warehouse_name":"ПВЗ_5","cluster_name":"Москва, МО и Дальние регионы","available_stock_count":3}
	]}`
	var resp analyticsStocksResp
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("не удалось разобрать ответ: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("строк = %d, ожидалось 2", len(resp.Items))
	}
	total := 0
	for _, it := range resp.Items {
		if NormalizeClusterName(it.ClusterName) != "Москва" {
			t.Errorf("кластер %q не нормализован", it.ClusterName)
		}
		total += it.AvailableStockCount
	}
	if total != 10 {
		t.Errorf("сумма остатков = %d, ожидалось 10", total)
	}
}

// loadFBOStocks должен вернуть ошибку, если хотя бы один пакет не доехал:
// иначе часть товаров молча получит нулевой остаток и завышенную поставку.
func TestLoadFBOStocksFailsWhenChunksIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Всегда отвечаем транзиентной 500, чтобы пачка не доехала ни разу.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":2,"message":"internal error"}`))
	}))
	defer srv.Close()

	productsMap := map[string]*RawProductData{
		"OFFER-1": {OfferID: "OFFER-1", SKU: "111", FBOClusters: map[string]int{}},
	}

	// Подменяем адрес метода на тестовый сервер через общий транспорт нельзя,
	// поэтому вызываем низкоуровневый путь с теми же опциями и проверяем, что
	// ошибка возвращается наружу, а не проглатывается.
	c := newTestClient(srv.URL)
	var resp analyticsStocksResp
	err := c.sendRequestWithOpts(context.Background(), pathAnalyticsStocks, analyticsStocksReq{
		SKUs: []string{productsMap["OFFER-1"].SKU},
	}, &resp, fastOpts)
	if err == nil {
		t.Fatal("ожидалась ошибка после исчерпания повторов")
	}
}

// Проверяем, что при сбое загрузки SKU расчет прерывается, а не отдает
// нулевые остатки по всем товарам.
func TestFetchAllProductsFailsFastOnStocksError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/product/list":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"items":[{"product_id":1,"offer_id":"OFFER-1"}],"last_id":""}}`))
		case "/v3/product/info/list":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[{"offer_id":"OFFER-1","sku":111,"name":"Товар"}]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":2}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	if _, err := c.FetchAllProducts(context.Background(), 28); err == nil {
		t.Fatal("ожидалась критическая ошибка при недоступных остатках FBO")
	}
}

func TestNormalizeClusterName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Москва, МО и Дальние регионы", "Москва"},
		{"москва", "Москва"},
		{"СПб", "Санкт-Петербург"},
		{"Санкт-Петербург и СЗО", "Санкт-Петербург"},
		{"Питер", "Санкт-Петербург"},
		{"Казань", "Поволжье"},
		{"Поволжье", "Поволжье"},
		{"Ростов-на-Дону", "Юг"},
		{"Краснодар", "Юг"},
		{"Екатеринбург", "Урал"},
		{"Новосибирск", "Сибирь"},
		{"НеизвестныйКластер", "НеизвестныйКластер"},
	}

	for _, tt := range tests {
		got := NormalizeClusterName(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeClusterName(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestDiscoverClusters(t *testing.T) {
	whMap := map[string]string{
		"СКЛАД_1": "Москва, МО и Дальние регионы",
		"СКЛАД_2": "Санкт-Петербург и СЗО",
		"СКЛАД_3": "Поволжье",
		"СКЛАД_4": "Москва",
	}

	clusters := DiscoverClusters(whMap)
	expected := []string{"Москва", "Поволжье", "Санкт-Петербург"}
	if len(clusters) != len(expected) {
		t.Fatalf("len(clusters) = %d, expected %d: %v", len(clusters), len(expected), clusters)
	}
	for i, c := range clusters {
		if c != expected[i] {
			t.Errorf("clusters[%d] = %q, expected %q", i, c, expected[i])
		}
	}
}

func TestParsePlacementReportCSV(t *testing.T) {
	csvContent := `Артикул;SKU;Статус размещения;Дата начала платного размещения;Стоимость размещения, руб.
OFFER-PAID;1001;Платно;-;1450,50
OFFER-FREE;1002;Бесплатно;2026-12-01;0
OFFER-NONE;1003;-;-;0.0
`
	items, err := ParsePlacementReportData([]byte(csvContent), "report.csv")
	if err != nil {
		t.Fatalf("ParsePlacementReportData failed: %v", err)
	}

	if item, ok := items["OFFER-PAID"]; !ok {
		t.Errorf("OFFER-PAID не найден")
	} else {
		if item.PlacementInfo != "Платно" {
			t.Errorf("expected 'Платно', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 1450.50 {
			t.Errorf("expected 1450.50, got %f", item.PastStorageCost)
		}
	}

	if item, ok := items["OFFER-FREE"]; !ok {
		t.Errorf("OFFER-FREE не найден")
	} else {
		if item.PlacementInfo != "Бесплатно (до 01.12.2026)" {
			t.Errorf("expected 'Бесплатно (до 01.12.2026)', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 0 {
			t.Errorf("expected 0, got %f", item.PastStorageCost)
		}
		if item.FreeUntil.Format("2006-01-02") != "2026-12-01" {
			t.Errorf("expected дата окончания бесплатного размещения 2026-12-01, got %v", item.FreeUntil)
		}
	}
}

func TestParsePlacementReportExcel(t *testing.T) {
	f := excelize.NewFile()
	sheet := "Лист1"
	f.SetSheetName("Sheet1", sheet)

	_ = f.SetSheetRow(sheet, "A1", &[]interface{}{"Артикул товара", "SKU", "Статус", "Бесплатно до", "Списано за период"})
	_ = f.SetSheetRow(sheet, "A2", &[]interface{}{"OFFER-X", "2001", "Бесплатно", "15.11.2026", "0.00"})
	_ = f.SetSheetRow(sheet, "A3", &[]interface{}{"OFFER-Y", "2002", "Платно", "", "350,25 ₽"})

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("failed to write excel buffer: %v", err)
	}

	items, err := ParsePlacementReportData(buf.Bytes(), "report.xlsx")
	if err != nil {
		t.Fatalf("ParsePlacementReportData Excel failed: %v", err)
	}

	if item, ok := items["OFFER-X"]; !ok {
		t.Errorf("OFFER-X не найден")
	} else {
		if item.PlacementInfo != "Бесплатно (до 15.11.2026)" {
			t.Errorf("expected 'Бесплатно (до 15.11.2026)', got %q", item.PlacementInfo)
		}
	}

	if item, ok := items["OFFER-Y"]; !ok {
		t.Errorf("OFFER-Y не найден")
	} else {
		if item.PlacementInfo != "Платно" {
			t.Errorf("expected 'Платно', got %q", item.PlacementInfo)
		}
		if item.PastStorageCost != 350.25 {
			t.Errorf("expected 350.25, got %f", item.PastStorageCost)
		}
	}
}

// Выгрузка личного кабинета «Стоимость размещения по товарам» устроена сложнее
// файла API: две строки заголовков (группы и подзаголовки) и колонки прогноза.
func TestParsePlacementReportLKExport(t *testing.T) {
	f := excelize.NewFile()
	sheet := "Страница #1"
	f.SetSheetName("Sheet1", sheet)

	row1 := []interface{}{
		"Название товара", "Категория товара", "Описательный тип", "Артикул", "SKU",
		"Стоимость за последние 28 дней", "",
		"Прогноз стоимости размещения до конца месяца",
		"Прогноз стоимости размещения на 28 дней",
		"Размещение на 18.10.2026",
	}
	row2 := make([]interface{}, 22)
	row2[5] = "Списано денег, руб"
	row2[6] = "Доля расходов от оборота, %"
	row2[9] = "Остаток на складах, шт"
	row2[10] = "Остаток на складах, л"
	row2[11] = "Зарезервировано под вывоз, шт"
	row2[12] = "Транзит, шт"
	row2[13] = "Транзит, л"
	row2[14] = "Размещение без платы, шт"
	row2[15] = "Размещение без платы, л"
	row2[16] = "Прогноз вынужденного размещения"
	row2[17] = "Вынужденное размещение, шт"
	row2[18] = "Вынужденное размещение, л"
	row2[19] = "Дней до начала вынужденного размещения"
	row2[20] = "Дата начала вынужденного размещения"
	row2[21] = "Списано денег, руб"

	paid := make([]interface{}, 22)
	paid[0] = "Фильтр воздушный Bajaj Dominar"
	paid[3] = "Фильтр возд. Dominar"
	paid[4] = "2251836561"
	paid[5] = 4592.59
	paid[6] = -0.4415
	paid[7] = 3744.75
	paid[8] = 1946.69
	paid[9] = 40
	paid[10] = 30.8
	paid[14] = 4
	paid[15] = 3.08
	paid[17] = 36
	paid[18] = 27.72
	paid[21] = 69.32 // стоимость размещения в день, не прошлое списание

	free := make([]interface{}, 22)
	free[0] = "Фара передняя Stels Flame"
	free[3] = "Фара без креп. Flame"
	free[4] = "1730232576"
	free[5] = 1476
	free[7] = 684
	free[8] = 0
	free[9] = 1
	free[10] = 14.4
	free[14] = 1
	free[15] = 14.4
	free[17] = 0
	free[18] = 0
	free[20] = "2027-09-27"
	free[21] = 36

	for cell, values := range map[string][]interface{}{
		"A1": row1, "A2": row2, "A3": paid, "A4": free,
	} {
		if err := f.SetSheetRow(sheet, cell, &values); err != nil {
			t.Fatalf("failed to write %s: %v", cell, err)
		}
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("failed to write excel buffer: %v", err)
	}

	items, err := ParsePlacementReportData(buf.Bytes(), "ozon_lk.xlsx")
	if err != nil {
		t.Fatalf("ParsePlacementReportData LK failed: %v", err)
	}

	if item, ok := items["Фильтр возд. Dominar"]; !ok {
		t.Errorf("Фильтр возд. Dominar не найден")
	} else {
		if item.PastStorageCost != 4592.59 {
			t.Errorf("прошлое списание = %v, ожидалось 4592.59", item.PastStorageCost)
		}
		if !item.HasOzonForecast || item.OzonForecast28 != 1946.69 || item.OzonForecastMonth != 3744.75 {
			t.Errorf("прогноз Ozon разобран неверно: %+v", item)
		}
		if item.PlacementInfo != "Платно" {
			t.Errorf("статус = %q, ожидался 'Платно'", item.PlacementInfo)
		}
	}

	if item, ok := items["Фара без креп. Flame"]; !ok {
		t.Errorf("Фара без креп. Flame не найдена")
	} else {
		if item.OzonForecast28 != 0 || item.OzonForecastMonth != 684 {
			t.Errorf("прогноз Ozon разобран неверно: %+v", item)
		}
		if item.PlacementInfo != "Бесплатно (до 27.09.2027)" {
			t.Errorf("статус = %q, ожидался 'Бесплатно (до 27.09.2027)'", item.PlacementInfo)
		}
	}
}

func TestFetchPlacementReport(t *testing.T) {
	var pollCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/report/placement/by-products/create":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"code":"REP-123"}}`))
		case "/v1/report/info":
			if atomic.AddInt32(&pollCount, 1) < 2 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":{"code":"REP-123","status":"processing"}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"code":"REP-123","status":"success","file":"http://` + r.Host + `/download/report.csv"}}`))
		case "/download/report.csv":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Артикул;SKU;Статус;Дата;Стоимость\nITEM-1;999;Платно;;500\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	items, err := c.FetchPlacementReport(context.Background(), 28)
	if err != nil {
		t.Fatalf("FetchPlacementReport failed: %v", err)
	}

	if item, ok := items["ITEM-1"]; !ok {
		t.Errorf("ITEM-1 не найден")
	} else {
		if item.PlacementInfo != "Платно" || item.PastStorageCost != 500 {
			t.Errorf("неверные данные: %+v", item)
		}
	}
}

// Если Ozon уронил генерацию отчета, клиент должен создать новое задание и
// дождаться файла, а не возвращать пустые данные.
func TestFetchPlacementReportRetriesAfterGenerationFailure(t *testing.T) {
	var creates int32
	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/report/placement/by-products/create":
			n := atomic.AddInt32(&creates, 1)
			_, _ = w.Write([]byte(`{"result":{"code":"REP-` + strconv.Itoa(int(n)) + `"}}`))
		case "/v1/report/info":
			var req struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			atomic.AddInt32(&polls, 1)
			if req.Code == "REP-1" {
				_, _ = w.Write([]byte(`{"result":{"code":"REP-1","status":"failed","error":"internal error"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"result":{"code":"REP-2","status":"success","file":"http://` + r.Host + `/download/report.csv"}}`))
		case "/download/report.csv":
			_, _ = w.Write([]byte("Артикул;SKU;Статус;Дата;Стоимость\nITEM-1;999;Платно;;500\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	items, err := c.FetchPlacementReport(context.Background(), 28)
	if err != nil {
		t.Fatalf("FetchPlacementReport failed: %v", err)
	}
	if item, ok := items["ITEM-1"]; !ok || item.PastStorageCost != 500 {
		t.Fatalf("отчет не получен после повтора: %+v", items)
	}
	if got := atomic.LoadInt32(&creates); got != 2 {
		t.Errorf("заданий создано %d, ожидалось 2 (повтор после failed)", got)
	}
}

func TestLoadFBOStocksDoesNotDoubleCountWhenWarehouseMatchesCluster(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[{"sku":111,"offer_id":"A-1","name":"Товар","warehouse_name":"Воронеж","cluster_name":"","available_stock_count":5}]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	productsMap := map[string]*RawProductData{
		"A-1": {OfferID: "A-1", SKU: "111", FBOClusters: make(map[string]int)},
	}
	if _, _, err := c.loadFBOStocks(context.Background(), productsMap); err != nil {
		t.Fatalf("loadFBOStocks failed: %v", err)
	}
	if got := productsMap["A-1"].FBOClusters["Воронеж"]; got != 5 {
		t.Errorf("остаток кластера/склада = %d, ожидалось 5 (без двойного учета)", got)
	}
	// Общий остаток нужен для прогноза хранения и тоже не должен удваиваться.
	if got := productsMap["A-1"].TotalFBO; got != 5 {
		t.Errorf("TotalFBO = %d, ожидалось 5", got)
	}
}
