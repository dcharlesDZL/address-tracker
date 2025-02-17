package main

import (
	"address-tracker/config"
	"address-tracker/solana"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/pressly/goose/v3"
	"log"
)

func main() {
	cfg, _ := config.LoadConfigFile("config/.env")
	monitor, err := solana.NewMonitor(cfg)
	if err != nil {
		log.Fatal(err)
	}
	//check db migration
	goose.SetBaseFS(nil)
	if err := goose.Up(monitor.DBClient.Conn.DB, "db/migrations"); err != nil {
		log.Fatalf("Failed to apply migrations: %v", err)
	}

	log.Println("Database migration completed successfully!")

	monitor.TelegramBot.Debug = false

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates, err := monitor.TelegramBot.GetUpdatesChan(u)
	go monitor.MonitorAddress()
	for update := range updates {
		if update.Message == nil {
			continue
		}
		if update.Message.IsCommand() {
			go monitor.HandleCommand(update.Message)
		}
	}
}
