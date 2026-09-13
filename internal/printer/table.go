package printer

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"CLIForOZON/internal/domain"
)

func PrintSummaryTable(rows []domain.ProductSupplyRow, selectedClusters []string) {
	if len(rows) == 0 {
		fmt.Println("Нет данных для отображения.")
		return
	}

	// Сортировка:
	// 1. Приоритет группы (OUT_OF_STOCK -> NEEDS_FBO -> NEEDS_FBS -> OK)
	// 2. Суммарная потребность в поставке (TotalSupply)
	// 3. Суммарные продажи по выбранным кластерам (TotalSales)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		if rows[i].TotalSupply != rows[j].TotalSupply {
			return rows[i].TotalSupply > rows[j].TotalSupply
		}
		return rows[i].TotalSales > rows[j].TotalSales
	})

	var outOfStock, needsFBO, needsFBS int
	for _, r := range rows {
		switch r.OverallStatus {
		case domain.StatusOutOfStock:
			outOfStock++
		case domain.StatusNeedsFBO:
			needsFBO++
		case domain.StatusNeedsFBS:
			needsFBS++
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 125))
	fmt.Printf(" СВОДКА ПОСТАВОК | Всего: %d | Закончились совсем: %d | Дефицит FBO: %d | Пустой FBS: %d\n",
		len(rows), outOfStock, needsFBO, needsFBS)
	fmt.Println(strings.Repeat("=", 125))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	// Динамический заголовок
	header := "АРТИКУЛ\tFBS"
	separator := "-------\t---"
	for _, c := range selectedClusters {
		shortName := truncate(c, 35)
		header += fmt.Sprintf("\t%s (СПРОС | ОСТ / ПОСТ)", shortName)
		separator += "\t--------------------------------"
	}
	header += "\tИТОГО ПОСТАВКА\tСТАТУС"
	separator += "\t--------------\t------"

	fmt.Fprintln(w, header)
	fmt.Fprintln(w, separator)

	for _, r := range rows {
		line := fmt.Sprintf("%s\t%d", truncate(r.OfferID, 40), r.CurrentFBS)
		for _, c := range selectedClusters {
			cd := r.ClusterData[c]
			line += fmt.Sprintf("\t%.2f | %d / %d", cd.DailySales, cd.StockFBO, cd.Supply)
		}
		line += fmt.Sprintf("\t%d\t%s", r.TotalSupply, r.OverallStatus)
		fmt.Fprintln(w, line)
	}

	_ = w.Flush()
	fmt.Println(strings.Repeat("=", 125) + "\n")
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}
