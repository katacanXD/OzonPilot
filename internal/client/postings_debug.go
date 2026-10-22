package client

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// SetDebugOffer включает подробный лог отправлений по одному артикулу.
func (c *OzonClient) SetDebugOffer(offerID string) {
	c.debugOffer = strings.TrimSpace(offerID)
}

// DebugOfferPostings печатает все отправления одного артикула за расширенное
// окно (90 дней) по обеим схемам: видно дату, статус, склады, кластеры и
// количество. Нужно, когда цифра в отчёте расходится с кабинетом.
func (c *OzonClient) DebugOfferPostings(ctx context.Context, windowDays int) {
	target := strings.ToLower(c.debugOffer)
	if target == "" {
		return
	}
	if windowDays <= 0 {
		windowDays = 28
	}

	const sweepDays = 90
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -sweepDays)
	windowStart := now.AddDate(0, 0, -windowDays)

	c.debugOfferScan(ctx, target, pathPostingFBOList, "FBO", since, now, windowStart)

	// FBS смотрим в /v4; старый /v3 дергаем только если v4 не ответил —
	// иначе это лишние запросы и риск поймать 429.
	if _, err := c.debugOfferScan(ctx, target, pathPostingFBSList, "FBS", since, now, windowStart); err != nil {
		c.debugOfferScan(ctx, target, pathPostingFBSListLegacy, "FBS(v3)", since, now, windowStart)
	}
}

// debugOfferScan листает один метод и печатает отправления нужного артикула.
func (c *OzonClient) debugOfferScan(ctx context.Context, target, path, scheme string, since, now, windowStart time.Time) (int, error) {
	found := 0

	err := c.forEachPostingPage(ctx, postingQuery{
		path:          path,
		since:         since,
		to:            now,
		withFinancial: true,
	}, func(items []postingItem, _ int) error {
		for _, post := range items {
			for _, prod := range post.Products {
				if strings.ToLower(strings.TrimSpace(prod.OfferID)) != target {
					continue
				}
				found++
				slog.Info("Диагностика артикула: отправление",
					"схема", scheme,
					"номер", post.PostingNumber,
					"статус", post.Status,
					"создано", post.CreatedAt,
					"в_окне_отчёта", postingInWindow(post, windowStart),
					"отгрузка", post.FinancialData.ClusterFrom,
					"назначение", post.FinancialData.ClusterTo,
					"склад", post.AnalyticsData.WarehouseName,
					"артикул_в_отправлении", prod.OfferID,
					"sku", prod.SKU,
					"шт", prod.Quantity)
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPostingPageStalled) {
		slog.Warn("Диагностика артикула: метод недоступен", "схема", scheme, "err", err)
	} else {
		err = nil
	}

	slog.Info("Диагностика артикула завершена",
		"артикул", c.debugOffer, "схема", scheme, "найдено_отправлений", found)
	return found, err
}

// postingInWindow сообщает, попадает ли отправление в окно отчёта.
func postingInWindow(post postingItem, windowStart time.Time) bool {
	raw := post.CreatedAt
	if raw == "" {
		raw = post.InProcessAt
	}
	created, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return !created.Before(windowStart)
}
