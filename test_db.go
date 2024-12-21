package main

import (
	"database/sql"
	"fmt"
	"log"
	_ "github.com/lib/pq"
)

func main() {
	connStr := "postgresql://postgres:postgres@10.18.149.71:5433/plex_watchlist?sslmode=disable"
	
	// Try to open a connection
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Error opening connection:", err)
	}
	defer db.Close()

	// Set a very low max open connections
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)

	// Try to ping
	err = db.Ping()
	if err != nil {
		log.Fatal("Error pinging database:", err)
	}

	fmt.Println("Successfully connected and pinged database")

	// Get max_connections setting
	var maxConn string
	err = db.QueryRow("SHOW max_connections").Scan(&maxConn)
	if err != nil {
		log.Fatal("Error getting max_connections:", err)
	}
	fmt.Println("max_connections:", maxConn)

	// Get current connection count
	var currentConn int
	err = db.QueryRow("SELECT count(*) FROM pg_stat_activity").Scan(&currentConn)
	if err != nil {
		log.Fatal("Error getting current connections:", err)
	}
	fmt.Println("current connections:", currentConn)
}
