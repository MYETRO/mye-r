package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"mye-r/internal/config"
	"mye-r/internal/database"
	"mye-r/internal/symlinker"

	"github.com/joho/godotenv"
)

func main() {
	// Parse command line arguments
	configFile := flag.String("config", "config.yaml", "Path to config file")
	envFile := flag.String("env", ".env", "Path to env file")
	flag.Parse()

	// Load environment variables
	if err := godotenv.Load(*envFile); err != nil {
		log.Println("Warning: .env file not found")
	}

	// Load configuration
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		fmt.Printf("Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize database connection
	db, err := database.NewDB(cfg.Database.URL)
	if err != nil {
		fmt.Printf("Failed to initialize database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Create symlinker
	sl := symlinker.New(cfg, db)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived shutdown signal. Gracefully stopping symlinker...")
		cancel()
	}()

	// Start symlinker
	if err := sl.Start(ctx); err != nil {
		if err == context.Canceled {
			fmt.Println("Symlinker stopped gracefully")
		} else {
			fmt.Printf("Failed to start symlinker: %v\n", err)
			os.Exit(1)
		}
	}
}
