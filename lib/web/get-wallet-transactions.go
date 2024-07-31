package web

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	types "github.com/HORNET-Storage/hornet-storage/lib"
	"github.com/HORNET-Storage/hornet-storage/lib/stores/graviton"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

func handleLatestTransactions(c *fiber.Ctx) error {
	// Initialize the Gorm database
	db, err := graviton.InitGorm()
	if err != nil {
		log.Printf("Failed to connect to the database: %v", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Internal Server Error")
	}

	// Get the latest 10 transactions
	var transactions []types.WalletTransactions
	result := db.Order("date desc").Limit(10).Find(&transactions)

	if result.Error != nil {
		log.Printf("Error querying transactions: %v", result.Error)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Database query error",
		})
	}

	// Get the latest Bitcoin rate
	var bitcoinRate types.BitcoinRate
	result = db.Order("timestamp desc").First(&bitcoinRate)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			log.Printf("No Bitcoin rate found, using default value")
			bitcoinRate.Rate = 0.0 // Set default rate
		} else {
			log.Printf("Error querying Bitcoin rate: %v", result.Error)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Database query error",
			})
		}
	}

	// Process each transaction to convert the value to USD
	for i, transaction := range transactions {
		// Trim whitespace and remove any commas from the value string
		cleanValue := strings.TrimSpace(strings.Replace(transaction.Value, ",", "", -1))

		// Parse the value as a float64 to handle decimal points
		satoshisFloat, err := strconv.ParseFloat(cleanValue, 64)
		if err != nil {
			log.Printf("Error converting value to float64: %v for value: '%s'", err, cleanValue)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Conversion error",
			})
		}

		// Convert to int64, rounding to the nearest Satoshi
		satoshis := int64(math.Round(satoshisFloat))

		// Convert Satoshis to USD
		usdValue := satoshiToUSD(bitcoinRate.Rate, satoshis)

		// Update the transaction value
		transactions[i].Value = fmt.Sprintf("%.2f", usdValue)
	}

	// Respond with the transactions
	return c.JSON(transactions)
}
