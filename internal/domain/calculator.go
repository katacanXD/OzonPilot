package domain

import (
	"math"
)

// CalculateProductRow формирует расчет по товару с учетом кластерных продаж.
func CalculateProductRow(
	offerID, name string,
	fbsStock int,
	fboClusters map[string]int,
	clusterSales map[string]float64, // продажи конкретно по каждому кластеру
	selectedClusters []string,
	targetDays int,
	useFBS bool,
	leadTimeDays float64,
) ProductSupplyRow {
	clusterData := make(map[string]ClusterStockData, len(selectedClusters))
	totalSupply := 0
	totalSales := 0.0
	anyClusterNeedsFBO := false
	allClustersEmpty := true

	for _, cluster := range selectedClusters {
		stockFBO := fboClusters[cluster]
		sales := clusterSales[cluster] // Скорость продаж именно в этом кластере
		totalSales += sales

		if stockFBO > 0 {
			allClustersEmpty = false
		}

		// Расчет целевого запаса под спрос кластера
		safetyStock := sales * leadTimeDays
		targetStock := (sales * float64(targetDays)) + safetyStock

		available := float64(stockFBO)
		if useFBS {
			available += float64(fbsStock)
		}

		deficit := targetStock - available
		recSupply := 0
		if deficit > 0 {
			recSupply = int(math.Ceil(deficit))
			anyClusterNeedsFBO = true
		}

		totalSupply += recSupply
		clusterData[cluster] = ClusterStockData{
			DailySales: sales,
			StockFBO:   stockFBO,
			Supply:     recSupply,
		}
	}

	// Определение статуса
	status := StatusOK
	priority := 4

	switch {
	case allClustersEmpty && fbsStock == 0 && totalSales > 0:
		status = StatusOutOfStock
		priority = 1
	case anyClusterNeedsFBO:
		status = StatusNeedsFBO
		priority = 2
	case fbsStock == 0:
		status = StatusNeedsFBS
		priority = 3
	}

	return ProductSupplyRow{
		OfferID:       offerID,
		Name:          name,
		TotalSales:    totalSales,
		CurrentFBS:    fbsStock,
		ClusterData:   clusterData,
		TotalSupply:   totalSupply,
		OverallStatus: status,
		Priority:      priority,
	}
}
