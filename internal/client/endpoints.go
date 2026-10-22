package client

// Пути методов Ozon. Держим их рядом, чтобы троттлинг и запросы использовали
// один и тот же ключ.
const (
	pathProductList     = "/v3/product/list"
	pathProductInfoList = "/v3/product/info/list"
	pathAnalyticsStocks = "/v1/analytics/stocks"
	pathPostingFBOList  = "/v3/posting/fbo/list"
	// /v3/posting/fbs/list Ozon отключает, актуальный метод — /v4. Формат ответа
	// у v4 плоский (postings в корне) и пагинация курсорная, но разбор умеет
	// оба варианта, поэтому v3 остается как резерв.
	pathPostingFBSList                  = "/v4/posting/fbs/list"
	pathPostingFBSListLegacy            = "/v3/posting/fbs/list"
	pathProductInfoStocks               = "/v4/product/info/stocks"
	pathReportPlacementByProductsCreate = "/v1/report/placement/by-products/create"
	pathReportInfo                      = "/v1/report/info"
)
