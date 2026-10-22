package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// maxResponseBytes ограничивает размер тела ответа от API/отчетов (64 МБ),
// защищая процесс от исчерпания памяти при некорректном ответе сервера.
const maxResponseBytes = 64 << 20

// readBoundedBody читает тело ответа, но не больше maxResponseBytes.
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

// sleepContext ждет d, но прерывается по отмене контекста.
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

// apiError — ответ Ozon со статусом вне диапазона 2xx. Тип нужен, чтобы
// вызывающий мог проверить статус через errors.As, не разбирая текст ошибки.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("ozon api error (status %d): %s", e.StatusCode, e.Body)
}

// hasStatus сообщает, что цепочка ошибок привела к ответу с этим HTTP-статусом.
func hasStatus(err error, status int) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
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

// minRequestInterval — общий лимит Ozon: 2 запроса в секунду на Client-Id.
// Переменная (а не константа) нужна, чтобы тесты могли отключить паузу.
var minRequestInterval = 600 * time.Millisecond

// slowEndpointIntervals задает увеличенную паузу для методов с более жестким
// лимитом частоты. Для /v1/analytics/stocks документация допускает примерно
// один запрос в минуту, поэтому без этой паузы запросы стабильно упираются в 429.
var slowEndpointIntervals = map[string]time.Duration{
	pathAnalyticsStocks: 6 * time.Second,
}

// rateLimiter выдерживает паузу между запросами, чтобы не упираться в лимит
// Ozon. Лимит общий на кабинет, поэтому ограничитель общий для процесса и
// удерживает блокировку на время ожидания — запросы идут строго последовательно.
type rateLimiter struct {
	mu       sync.Mutex
	last     time.Time
	lastSlow map[string]time.Time
}

var ozonLimiter = &rateLimiter{lastSlow: make(map[string]time.Time)}

func (l *rateLimiter) wait(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	wait := minRequestInterval - now.Sub(l.last)

	slow, isSlow := slowEndpointIntervals[path]
	if isSlow {
		if slowWait := slow - now.Sub(l.lastSlow[path]); slowWait > wait {
			wait = slowWait
		}
	}

	if wait > 0 {
		time.Sleep(wait)
	}

	now = time.Now()
	l.last = now
	if isSlow {
		l.lastSlow[path] = now
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
			ozonLimiter.wait(path)
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

		lastErr = &apiError{StatusCode: resp.StatusCode, Body: string(respBytes)}

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
