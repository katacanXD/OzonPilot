package domain

const (
	StatusOutOfStock = "OUT_OF_STOCK" // Закончился и на FBO, и на FBS при наличии спроса
	StatusNeedsFBO   = "NEEDS_FBO"    // Требуется поставка на FBO
	StatusNeedsFBS   = "NEEDS_FBS"    // Нет остатка на складе FBS
	StatusOK         = "OK"           // Запаса достаточно
)

type ClusterStockData struct {
	PeriodSales float64 // Продажи за весь выбранный период
	DailySales  float64 // Среднесуточные продажи для расчета поставки
	StockFBO    int     // Остаток на складах Ozon в этом кластере
	Supply      int     // Рекомендация к поставке в этот кластер
}

type ProductSupplyRow struct {
	OfferID       string
	Name          string
	TotalSales    float64                     // Суммарные продажи по выбранным кластерам
	CurrentFBS    int                         // Текущий остаток селлера (FBS)
	ClusterData   map[string]ClusterStockData // Разрез по кластерам
	TotalSupply   int                         // Суммарная поставка по всем кластерам
	OverallStatus string                      // Общий статус
	Priority      int                         // Приоритет срочности
	PlacementInfo string                      // Размещение FBO ("Платно", "Бесплатно (до ДД.ММ.ГГГГ)", etc.)
	// TotalFBOStock — остаток на всех складах Ozon (не только в выбранных
	// кластерах): хранение начисляется за весь остаток, поэтому прогноз
	// считается от него.
	TotalFBOStock int
	// TotalPeriodSales — продажи за весь период по всем складам Ozon. Вместе с
	// остатком задает средний остаток за прошедший период, от которого
	// оценивается тариф хранения.
	TotalPeriodSales float64
	// StorageCost — прошлые списания за хранение по отчету Ozon за отчетный
	// период, ₽. Показывается рядом с прогнозом: «сколько было» и «сколько
	// еще будет».
	StorageCost float64
	// StorageForecast — прогноз списания за хранение на будущий период, ₽.
	// Считается программой и используется, только если в отчете Ozon нет его
	// собственного прогноза.
	StorageForecast float64
	// OzonStorageForecast28 и OzonStorageForecastMonth — прогноз Ozon из
	// выгрузки личного кабинета: «на 28 дней» и «до конца месяца», ₽.
	OzonStorageForecast28    float64
	OzonStorageForecastMonth float64
	// HasOzonStorageForecast — в отчете были колонки прогноза Ozon.
	HasOzonStorageForecast bool
}
