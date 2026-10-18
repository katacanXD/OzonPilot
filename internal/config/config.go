package config

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// apiBaseURL вынесен в переменную, чтобы тесты могли подменить адрес Ozon.
var apiBaseURL = "https://api-seller.ozon.ru"

const maxAuthAttempts = 3

type AppConfig struct {
	ClientID   string `mapstructure:"client_id"`
	APIKey     string `mapstructure:"api_key"`
	WindowDays int    `mapstructure:"prodazhi"`
	TargetDays int    `mapstructure:"postavka"`
	Clusters   string `mapstructure:"clusters"`
}

func GetConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	appDir := filepath.Join(configDir, "CLIForOZON")
	if err := os.MkdirAll(appDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	return appDir, nil
}

func initViper() error {
	appDir, err := GetConfigDir()
	if err != nil {
		return err
	}

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.SetConfigPermissions(0600)
	viper.AddConfigPath(appDir)

	// Set default values
	viper.SetDefault("prodazhi", 28)
	viper.SetDefault("postavka", 25)
	viper.SetDefault("clusters", "Москва")

	configFilePath := filepath.Join(appDir, "config.yaml")
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if err := viper.SafeWriteConfigAs(configFilePath); err != nil {
			return fmt.Errorf("failed to create default config file: %w", err)
		}
	}

	return nil
}

func LoadConfig() (*AppConfig, error) {
	if err := initViper(); err != nil {
		return nil, err
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg AppConfig
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &cfg, nil
}

func SaveConfig(cfg *AppConfig) error {
	viper.Set("client_id", cfg.ClientID)
	viper.Set("api_key", cfg.APIKey)
	// Ключи должны совпадать с тегами AppConfig, иначе значения не читаются обратно.
	viper.Set("prodazhi", cfg.WindowDays)
	viper.Set("postavka", cfg.TargetDays)
	viper.Set("clusters", cfg.Clusters)

	if err := viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

func ValidateOrPromptKeys(cfg *AppConfig) error {
	client := &http.Client{Timeout: 10 * time.Second}
	reader := bufio.NewReader(os.Stdin)

	for attempt := 1; attempt <= maxAuthAttempts; attempt++ {
		prompted := false
		for cfg.ClientID == "" || cfg.APIKey == "" {
			if err := promptCredentials(reader, cfg); err != nil {
				return err
			}
			prompted = true
		}

		reqBody := bytes.NewBufferString(`{"filter":{"visibility":"ALL"},"limit":1}`)
		req, err := http.NewRequest(http.MethodPost, apiBaseURL+"/v3/product/list", reqBody)
		if err != nil {
			return fmt.Errorf("failed to create HTTP request: %w", err)
		}

		req.Header.Set("Client-Id", cfg.ClientID)
		req.Header.Set("Api-Key", cfg.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("network error during credential validation: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			if err := SaveConfig(cfg); err != nil {
				if prompted {
					return err
				}
				// Ключи уже лежали в конфиге: недоступность файла на запись не повод
				// не строить отчет (read-only профиль, чужой ACL, песочница).
				slog.Warn("Не удалось обновить файл конфигурации, работаю с текущими ключами", "err", err)
			}
			return nil
		case http.StatusUnauthorized, http.StatusForbidden:
			fmt.Println("\n[!] Invalid keys! Ozon API denied access (401/403). Please re-enter credentials.")
			cfg.ClientID = ""
			cfg.APIKey = ""
		default:
			return fmt.Errorf("unexpected API status code: %d", resp.StatusCode)
		}
	}

	return fmt.Errorf("превышено число попыток авторизации Ozon (%d)", maxAuthAttempts)
}

func promptCredentials(reader *bufio.Reader, cfg *AppConfig) error {
	fmt.Print("Enter Ozon Client-Id: ")
	clientID, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read Client-Id: %w", err)
	}
	cfg.ClientID = clientID

	fmt.Print("Enter Ozon Api-Key: ")
	apiKey, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("failed to read Api-Key: %w", err)
	}
	cfg.APIKey = apiKey
	return nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || len(line) == 0) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
