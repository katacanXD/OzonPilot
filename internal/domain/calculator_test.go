package domain

import "testing"

func TestCalculateProductRowStatusesAndSupply(t *testing.T) {
	clusters := []string{"Москва"}

	// 1. OUT_OF_STOCK: пустой FBO, пустой FBS, есть спрос
	out := CalculateProductRow(
		"OFFER-OUT", "Товар 1", 0,
		map[string]int{"Москва": 0},
		map[string]float64{"Москва": 2.0},
		map[string]float64{"Москва": 56.0},
		clusters, 25, 3.0,
	)
	if out.OverallStatus != StatusOutOfStock || out.Priority != 1 {
		t.Errorf("ожидался статус %s (приоритет 1), получено %s (%d)", StatusOutOfStock, out.OverallStatus, out.Priority)
	}
	// target = 2.0 * (25 + 3) = 56
	if out.TotalSupply != 56 {
		t.Errorf("ожидалась поставка 56, получено %d", out.TotalSupply)
	}

	// 2. NEEDS_FBO: есть остаток FBS, но дефицит на FBO
	needsFBO := CalculateProductRow(
		"OFFER-FBO", "Товар 2", 10,
		map[string]int{"Москва": 10},
		map[string]float64{"Москва": 1.0},
		map[string]float64{"Москва": 28.0},
		clusters, 25, 3.0,
	)
	if needsFBO.OverallStatus != StatusNeedsFBO || needsFBO.Priority != 2 {
		t.Errorf("ожидался статус %s (приоритет 2), получено %s (%d)", StatusNeedsFBO, needsFBO.OverallStatus, needsFBO.Priority)
	}
	if needsFBO.TotalSupply != 18 {
		t.Errorf("ожидалась поставка 18, получено %d", needsFBO.TotalSupply)
	}

	// 3. NEEDS_FBS: на FBO достаточно, но FBS == 0
	needsFBS := CalculateProductRow(
		"OFFER-FBS", "Товар 3", 0,
		map[string]int{"Москва": 50},
		map[string]float64{"Москва": 1.0},
		map[string]float64{"Москва": 28.0},
		clusters, 25, 3.0,
	)
	if needsFBS.OverallStatus != StatusNeedsFBS || needsFBS.Priority != 3 {
		t.Errorf("ожидался статус %s (приоритет 3), получено %s (%d)", StatusNeedsFBS, needsFBS.OverallStatus, needsFBS.Priority)
	}
	if needsFBS.TotalSupply != 0 {
		t.Errorf("ожидалась поставка 0, получено %d", needsFBS.TotalSupply)
	}

	// 4. OK: запаса достаточно и на FBO, и на FBS
	okRow := CalculateProductRow(
		"OFFER-OK", "Товар 4", 5,
		map[string]int{"Москва": 50},
		map[string]float64{"Москва": 1.0},
		map[string]float64{"Москва": 28.0},
		clusters, 25, 3.0,
	)
	if okRow.OverallStatus != StatusOK || okRow.Priority != 4 {
		t.Errorf("ожидался статус %s (приоритет 4), получено %s (%d)", StatusOK, okRow.OverallStatus, okRow.Priority)
	}
}
