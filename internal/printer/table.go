package printer

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"CLIForOZON/internal/domain"
)

func PrintSummaryTable(rows []domain.ProductSupplyRow, selectedClusters []string) {
	if len(rows) == 0 {
		fmt.Println("Нет данных для отображения.")
		return
	}

	sortRows(rows)

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

	hasOzonForecast := false
	for _, r := range rows {
		if r.HasOzonStorageForecast {
			hasOzonForecast = true
			break
		}
	}

	var header, separator strings.Builder
	header.WriteString("АРТИКУЛ\tFBS\tРАЗМЕЩЕНИЕ\tСПИСАНО (₽)")
	separator.WriteString("-------\t---\t----------\t-----------")
	if hasOzonForecast {
		header.WriteString("\tПРОГНОЗ OZON 28 ДН (₽)\tДО КОНЦА МЕСЯЦА (₽)")
		separator.WriteString("\t--------------------\t--------------------")
	} else {
		header.WriteString(fmt.Sprintf("\tОЦЕНКА (%d ДН, ₽)", domain.DefaultStorageForecastDays))
		separator.WriteString("\t-----------------")
	}
	for _, c := range selectedClusters {
		fmt.Fprintf(&header, "\t%s (ПРОДАЖИ | ОСТ / ПОСТ)", truncate(c, 35))
		separator.WriteString("\t------------------------")
	}
	header.WriteString("\tСТАТУС")
	separator.WriteString("\t------")

	fmt.Fprintln(w, header.String())
	fmt.Fprintln(w, separator.String())

	var line strings.Builder
	for _, r := range rows {
		line.Reset()
		placement := r.PlacementInfo
		if placement == "" {
			placement = "-"
		}
		fmt.Fprintf(&line, "%s\t%d\t%s\t%.2f", truncate(r.OfferID, 30), r.CurrentFBS, truncate(placement, 22), r.StorageCost)
		if hasOzonForecast {
			fmt.Fprintf(&line, "\t%.2f\t%.2f", r.OzonStorageForecast28, r.OzonStorageForecastMonth)
		} else {
			fmt.Fprintf(&line, "\t%.2f", r.StorageForecast)
		}
		for _, c := range selectedClusters {
			cd := r.ClusterData[c]
			fmt.Fprintf(&line, "\t%.0f | %d / %d", cd.PeriodSales, cd.StockFBO, cd.Supply)
		}
		fmt.Fprintf(&line, "\t%s", r.OverallStatus)
		fmt.Fprintln(w, line.String())
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
