package web

import (
	"log"
	"time"

	types "github.com/HORNET-Storage/hornet-storage/lib"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	// Import the package for InitGorm
)

func handleTransactions(c *fiber.Ctx) error {
	var transactions []map[string]interface{}
	log.Println("Transactions request received")

	// Parse the JSON body into the slice of maps
	if err := c.BodyParser(&transactions); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Cannot parse JSON",
		})
	}

	// Initialize the Gorm database
	db, err := graviton.InitGorm()
	if err != nil {
		log.Printf("Failed to connect to the database: %v", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Internal Server Error")
	}

	for _, transaction := range transactions {
		wtxid, ok := transaction["WTxId"].(string)
		if !ok {
			log.Printf("Invalid address format: %v", transaction["address"])
			continue
		}

		dateStr, ok := transaction["date"].(string)
		if !ok {
			log.Printf("Invalid date format: %v", transaction["date"])
			continue
		}

		date, err := time.Parse("2006-01-02 15:04:05", dateStr)
		if err != nil {
			log.Printf("Error parsing date: %v", err)
			continue
		}

		output, ok := transaction["output"].(string)
		if !ok {
			log.Printf("Invalid output format: %v", transaction["output"])
			continue
		}

		value, ok := transaction["value"].(string)
		if !ok {
			log.Printf("Invalid value format: %v", transaction["value"])
			continue
		}

		var existingTransaction types.WalletTransactions
		result := db.Where("witness_tx_id = ? AND date = ? AND output = ? AND value = ?", wtxid, date, output, value).First(&existingTransaction)

		if result.Error == nil {
			// Transaction already exists, skip it
			log.Printf("Duplicate transaction found, skipping: %v", transaction)
			continue
		}

		if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
			log.Printf("Error querying transaction: %v", result.Error)
			continue
		}

		// Create a new transaction
		newTransaction := types.WalletTransactions{
			WitnessTxId: wtxid,
			Date:        date,
			Output:      output,
			Value:       value,
		}
		if err := db.Create(&newTransaction).Error; err != nil {
			log.Printf("Error saving new transaction: %v", err)
			continue
		}
	}

	// Respond with a success message
	return c.JSON(fiber.Map{
		"status":  "success",
		"message": "Transactions received and processed successfully",
	})
}
