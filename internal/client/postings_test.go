package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
