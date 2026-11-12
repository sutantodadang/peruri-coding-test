package peruri_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"peruri/internal/config"
)

func TestLoadEnv(t *testing.T) {
	t.Run("local file and process precedence", func(t *testing.T) {
		t.Setenv("PERURI_TEST_FILE_VALUE", "")
		check(t, os.Unsetenv("PERURI_TEST_FILE_VALUE"))
		t.Setenv("PERURI_TEST_PROCESS_VALUE", "from-process")
		t.Setenv("PERURI_TEST_EMPTY_VALUE", "")
		path := filepath.Join(t.TempDir(), ".env")
		check(t, os.WriteFile(path, []byte("# local settings\r\nPERURI_TEST_FILE_VALUE='value with spaces # literal'\r\nPERURI_TEST_PROCESS_VALUE=from-file\r\nPERURI_TEST_EMPTY_VALUE=from-file\r\n"), 0600))
		check(t, config.LoadEnv(path))
		if os.Getenv("PERURI_TEST_FILE_VALUE") != "value with spaces # literal" || os.Getenv("PERURI_TEST_PROCESS_VALUE") != "from-process" || os.Getenv("PERURI_TEST_EMPTY_VALUE") != "" {
			t.Fatal("dotenv values or process precedence changed")
		}
	})
	t.Run("optional missing file", func(t *testing.T) {
		check(t, config.LoadEnv(filepath.Join(t.TempDir(), ".env")))
	})
	t.Run("invalid file does not expose credentials", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".env")
		check(t, os.WriteFile(path, []byte("TOKEN='do-not-log-this-secret"), 0600))
		err := config.LoadEnv(path)
		if err == nil || strings.Contains(err.Error(), "do-not-log-this-secret") {
			t.Fatal("invalid dotenv input must fail without logging its contents")
		}
	})
}

func TestDemoConfigurationMatchesListeners(t *testing.T) {
	values, err := godotenv.Read("../.env.example")
	check(t, err)
	for endpoint, listener := range map[string]string{"CALLBACK_URL": "HTTP_ADDR", "PRINCIPAL_URL": "HTTP_ADDR_PRINCIPAL"} {
		parsed, err := url.Parse(values[endpoint])
		check(t, err)
		if parsed.Host != values[listener] {
			t.Fatalf("%s targets %s, listener %s uses %s", endpoint, parsed.Host, listener, values[listener])
		}
	}
}
