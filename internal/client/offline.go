package client

import (
	"encoding/json"
	"fmt"
	"os"

	"CLIForOZON/internal/domain"
)

// FetchProductsFromJSON читает заранее посчитанные строки отчета для
// offline-режима (-file): расчета поставок по ним не делается.
func FetchProductsFromJSON(filePath string) ([]domain.ProductSupplyRow, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file failed: %w", err)
	}

	var rows []domain.ProductSupplyRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal json failed: %w", err)
	}
	return rows, nil
}
