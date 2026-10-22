package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
