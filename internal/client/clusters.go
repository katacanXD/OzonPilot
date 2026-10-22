package client

import (
	"sort"
	"strings"
)

// warehouseClusterHints — резервное соответствие «склад → кластер»: нужно,
// когда склад из истории отправлений отсутствует в карте, полученной из
// /v1/analytics/stocks. Кластеры и склады — по таблице Ozon
// (seller-edu.ozon.ru/libra/fbo/warehouses/table-klastery), без укрупнения.
var warehouseClusterHints = []struct {
	cluster  string
	keywords []string
}{
	{"Москва", []string{"вешки", "ватутинки", "гривно", "домодедово", "жуковский", "купавна", "никольское", "ногинск", "павло слободское", "павловская слобода", "петровское", "петровск", "пушкино", "радумля", "софьино", "хоругвино", "москва"}},
	{"Санкт-Петербург", []string{"петербург", "спб", "колпино", "шушары", "волхонка", "бугры", "порошкино", "шоссейная"}},
	{"Тверь", []string{"тверь"}},
	{"Воронеж", []string{"воронеж"}},
	{"Дальний Восток", []string{"дальний восток", "артём", "артем", "владивосток", "хабаровск"}},
	{"Екатеринбург", []string{"екатеринбург"}},
	{"Пермь", []string{"пермь"}},
	{"Тюмень", []string{"тюмень"}},
	{"Казань", []string{"казан", "казат", "кзн", "столбище", "нижний новгород"}},
	{"Самара", []string{"самара"}},
	{"Уфа", []string{"уфа"}},
	{"Оренбург", []string{"оренбург"}},
	{"Краснодар", []string{"краснодар", "адыгейск", "адыгея", "новороссийск", "южный обход"}},
	{"Ростов", []string{"ростов"}},
	{"Саратов", []string{"саратов", "волгоград"}},
	{"Невинномысск", []string{"невинномысск"}},
	{"Махачкала", []string{"махачкала"}},
	{"Красноярск", []string{"красноярск"}},
	{"Новосибирск", []string{"новосибирск", "кемерово"}},
	{"Омск", []string{"омск"}},
	{"Ярославль", []string{"ярославль"}},
	{"Калининград", []string{"калининград"}},
	{"Беларусь", []string{"минск", "орша"}},
	{"Астана", []string{"астана"}},
	{"Алматы", []string{"алматы"}},
	{"Армения", []string{"ереван"}},
}

// clusterAliases — только синонимы длинных имён Ozon и коротких форм, которыми
// удобно задавать кластеры в -clusters и config.yaml. Остальные кластеры
// используются так, как их называет Ozon: иначе колонки в отчёте разойдутся с
// кабинетом (Казань, Ростов, Екатеринбург — самостоятельные кластеры).
var clusterAliases = map[string]string{
	"москва, мо и дальние регионы": "Москва",
	"москва": "Москва",
	"санкт-петербург и сзо": "Санкт-Петербург",
	"санкт-петербург":       "Санкт-Петербург",
	"спб":                   "Санкт-Петербург",
	"питер":                 "Санкт-Петербург",
}

// NormalizeClusterName приводит название кластера Ozon к короткому имени,
// которое используется в CLI. Применяется и к данным из API, и к списку
// кластеров из флага -clusters, чтобы ключи совпадали.
func NormalizeClusterName(name string) string {
	trimmed := strings.TrimSpace(name)
	if alias, ok := clusterAliases[strings.ToLower(trimmed)]; ok {
		return alias
	}
	return trimmed
}

// matchWarehouseClusterFallback используется для складов, отсутствующих
// в карте «склад → кластер», полученной из остатков FBO.
func matchWarehouseClusterFallback(whName string) string {
	n := strings.ToLower(whName)
	for _, hint := range warehouseClusterHints {
		for _, kw := range hint.keywords {
			if strings.Contains(n, kw) {
				return hint.cluster
			}
		}
	}
	return ""
}

// DiscoverClusters возвращает отсортированный список уникальных кластеров
// из карты соответствия складов.
func DiscoverClusters(warehouseToCluster map[string]string) []string {
	seen := make(map[string]bool)
	var clusters []string
	for _, c := range warehouseToCluster {
		normalized := NormalizeClusterName(c)
		if normalized != "" && !seen[normalized] {
			seen[normalized] = true
			clusters = append(clusters, normalized)
		}
	}
	sort.Strings(clusters)
	return clusters
}

// DiscoveredClusters возвращает кластеры, где есть остатки или были заказы.
// Кластер только с заказами тоже важен: спрос есть, а остатков нет.
func (c *OzonClient) DiscoveredClusters() []string {
	seen := make(map[string]bool)
	clusters := make([]string, 0, len(c.warehouseToCluster)+len(c.salesClusters))
	for _, cluster := range DiscoverClusters(c.warehouseToCluster) {
		if !seen[cluster] {
			seen[cluster] = true
			clusters = append(clusters, cluster)
		}
	}
	for cluster := range c.salesClusters {
		if cluster != "" && !seen[cluster] {
			seen[cluster] = true
			clusters = append(clusters, cluster)
		}
	}
	sort.Strings(clusters)
	return clusters
}

// normalizeWarehouseKey нужен, потому что разные методы Ozon отдают названия
// складов в разном регистре (например, «Новосибирск_РФЦ_НОВЫЙ» и
// «НОВОСИБИРСК_РФЦ_НОВЫЙ»).
func normalizeWarehouseKey(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
