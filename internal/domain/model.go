package domain

const (
	StatusOutOfStock = "OUT_OF_STOCK" // Закончился и на FBO, и на FBS при наличии спроса
	StatusNeedsFBO   = "NEEDS_FBO"    // Требуется поставка на FBO
	StatusNeedsFBS   = "NEEDS_FBS"    // Нет остатка на складе FBS
	StatusOK         = "OK"           // Запаса достаточно
)

// ClusterStockData хранит статистику конкретного кластера: спрос, остаток и рекомендацию.
type ClusterStockData struct {
	DailySales float64 // Продажи именно в этом кластере (шт/день)
	StockFBO   int     // Остаток на складах Ozon в этом кластере
	Supply     int     // Рекомендация к поставке в этот кластер
}

// ProductSupplyRow — строка таблицы для одного товара.
type ProductSupplyRow struct {
	OfferID       string
	Name          string
	TotalSales    float64                     // Суммарные продажи по выбранным кластерам
	CurrentFBS    int                         // Текущий остаток селлера (FBS)
	ClusterData   map[string]ClusterStockData // Разрез по кластерам
	TotalSupply   int                         // Суммарная поставка по всем кластерам
	OverallStatus string                      // Общий статус
	Priority      int                         // Приоритет срочности
}
