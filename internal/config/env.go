package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// LoadEnv supplements process configuration with an optional local dotenv file.
func LoadEnv(path string) error {
	if err := godotenv.Load(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Parser errors can contain credential values from the source line.
		return fmt.Errorf("cannot load %s; check dotenv syntax and file permissions", path)
	}
	return nil
}
