package config

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Ключи уже сохранены, а файл конфигурации недоступен на запись: запуск не должен падать.
func TestValidateOrPromptKeysToleratesUnwritableConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	restoreURL := apiBaseURL
	apiBaseURL = server.URL
	defer func() { apiBaseURL = restoreURL }()

	appDir := filepath.Join(t.TempDir(), "CLIForOZON")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatalf("не удалось создать каталог конфига: %v", err)
	}
	t.Setenv("APPDATA", filepath.Dir(appDir))

	configPath := filepath.Join(appDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("client_id: \"1\"\napi_key: \"key\"\n"), 0644); err != nil {
		t.Fatalf("не удалось записать конфиг: %v", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ClientID == "" || cfg.APIKey == "" {
		t.Fatalf("ключи не прочитаны из конфига: %+v", cfg)
	}

	// Атрибут «только чтение» — та же ошибка доступа, что и на read-only профиле.
	if err := os.Chmod(configPath, 0444); err != nil {
		t.Fatalf("не удалось сделать конфиг доступным только для чтения: %v", err)
	}
	defer func() { _ = os.Chmod(configPath, 0644) }()

	if err := ValidateOrPromptKeys(cfg); err != nil {
		t.Fatalf("недоступный на запись конфиг не должен ронять запуск с готовыми ключами: %v", err)
	}
}
