package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

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
