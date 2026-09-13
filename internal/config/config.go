package config

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

// AppConfig holds the application configuration parameters.
type AppConfig struct {
	ClientID   string `mapstructure:"client_id"`
	APIKey     string `mapstructure:"api_key"`
	WindowDays int    `mapstructure:"window_days"`
	TargetDays int    `mapstructure:"target_days"`
	UseFBS     bool   `mapstructure:"use_fbs"`
}

// GetConfigDir returns the cross-platform directory for configuration files.
// On Windows, it points to %APPDATA%\CLIForOZON.
func GetConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	appDir := filepath.Join(configDir, "CLIForOZON")
	if err := os.MkdirAll(appDir, 0755); err != nil {
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
	viper.AddConfigPath(appDir)

	// Set default values
	viper.SetDefault("window_days", 14)
	viper.SetDefault("target_days", 30)
	viper.SetDefault("use_fbs", false)

	configFilePath := filepath.Join(appDir, "config.yaml")
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if err := viper.SafeWriteConfigAs(configFilePath); err != nil {
			return fmt.Errorf("failed to create default config file: %w", err)
		}
	}

	return nil
}

// LoadConfig initializes Viper, reads the configuration file, and unmarshals it into AppConfig.
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

// SaveConfig updates Viper with values from AppConfig and persists them to disk.
func SaveConfig(cfg *AppConfig) error {
	viper.Set("client_id", cfg.ClientID)
	viper.Set("api_key", cfg.APIKey)
	viper.Set("window_days", cfg.WindowDays)
	viper.Set("target_days", cfg.TargetDays)
	viper.Set("use_fbs", cfg.UseFBS)

	if err := viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// ValidateOrPromptKeys verifies Ozon API credentials via HTTP POST and prompts the user interactively if invalid.
func ValidateOrPromptKeys(cfg *AppConfig) error {
	for cfg.ClientID == "" || cfg.APIKey == "" {
		if err := promptCredentials(cfg); err != nil {
			return err
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	reqBody := bytes.NewBufferString(`{"filter":{"visibility":"ALL"},"limit":1}`)

	req, err := http.NewRequest(http.MethodPost, "https://api-seller.ozon.ru/v3/product/list", reqBody)
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
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return SaveConfig(cfg)
	case http.StatusUnauthorized, http.StatusForbidden:
		fmt.Println("\n[!] Invalid keys! Ozon API denied access (401/403). Please re-enter credentials.")
		cfg.ClientID = ""
		cfg.APIKey = ""
		return ValidateOrPromptKeys(cfg)
	default:
		return fmt.Errorf("unexpected API status code: %d", resp.StatusCode)
	}
}

func promptCredentials(cfg *AppConfig) error {
	fmt.Print("Enter Ozon Client-Id: ")
	if _, err := fmt.Scanln(&cfg.ClientID); err != nil {
		return fmt.Errorf("failed to read Client-Id: %w", err)
	}

	fmt.Print("Enter Ozon Api-Key: ")
	if _, err := fmt.Scanln(&cfg.APIKey); err != nil {
		return fmt.Errorf("failed to read Api-Key: %w", err)
	}
	return nil
}
