package config

import (
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"log"
)

type Config struct {
	// db
	DBName     string `envconfig:"POSTGRES_DBNAME"`
	DBHost     string `envconfig:"POSTGRES_HOST"`
	DBPort     uint64 `envconfig:"POSTGRES_PORT"`
	DBUser     string `envconfig:"POSTGRES_USER"`
	DBPassword string `envconfig:"POSTGRES_PASSWORD"`
	// solana
	SolanaRPCWebsocket string `envconfig:"SOL_RPC_WS"`
	SolanaRPCHttp      string `envconfig:"SOL_RPC_HTTP"`
	// telegram bot
	TelegramBotAPI  string `envconfig:"TG_BOT_API"`
	TelegramBotName string `envconfig:"TG_BOT_NAME"`
}

func LoadConfigFile(filename string) (*Config, error) {
	err := godotenv.Load(filename)
	if err != nil {
		log.Fatalf("加载 .env 文件失败: %v", err)
	}
	var cfg Config
	err = envconfig.Process("", &cfg)
	if err != nil {
		log.Fatalf("加载环境变量失败: %v", err)
	}
	return &cfg, nil
}
