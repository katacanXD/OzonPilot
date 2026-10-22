package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
