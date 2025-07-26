package config

import (
	"log"

	"github.com/joho/godotenv"
)

// LoadEnv loads environment variables from the .env file in the project root.
func LoadEnv() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("[WARN] .env file not found or failed to load")
	} else {
		log.Println("[INFO] .env loaded successfully")
	}
}
