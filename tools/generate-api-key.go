// Package main provides a tool to generate HOSPITUS API keys
package main

import (
	"fmt"
	"os"

	"github.com/hospitus/hospitus/internal/auth"
)

func main() {
	// Generate a new API key
	apiKey, err := auth.GenerateAPIKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating API key: %v\n", err)
		os.Exit(1)
	}

	// Generate hash for storage
	hash, err := auth.HashAPIKey(apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing API key: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=================================================================")
	fmt.Println("HOSPITUS API Key Generated Successfully")
	fmt.Println("=================================================================")
	fmt.Println()
	fmt.Printf("API Key: %s\n", apiKey)
	fmt.Println()
	fmt.Println("IMPORTANT: Save this key securely!")
	fmt.Println("You will not be able to see it again.")
	fmt.Println()
	fmt.Println("To use this key:")
	fmt.Println("1. Add to your HOSPITUS configuration:")
	fmt.Println("   APIKeys: []string{\"" + apiKey + "\"}")
	fmt.Println()
	fmt.Println("2. Or set as environment variable:")
	fmt.Println("   export HOSPITUS_API_KEYS=\"" + apiKey + "\"")
	fmt.Println()
	fmt.Println("3. Use in API requests:")
	fmt.Println("   curl -H \"X-API-Key: " + apiKey + "\" \\")
	fmt.Println("     https://hospitus.example.com/api/v1/instances")
	fmt.Println()
	fmt.Println("=================================================================")
	fmt.Println()
	fmt.Println("Bcrypt Hash (for reference):")
	fmt.Println(hash)
	fmt.Println()
	fmt.Println("Security Tips:")
	fmt.Println("- Never commit API keys to version control")
	fmt.Println("- Rotate keys every 90 days")
	fmt.Println("- Use separate keys for each environment")
	fmt.Println("- Store keys in a secure secrets manager")
	fmt.Println("=================================================================")
}
