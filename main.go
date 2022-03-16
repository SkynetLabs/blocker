package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/SkynetLabs/blocker/syncer"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	// defaultSkydHost is where we connect to skyd unless overwritten by
	// "API_HOST" environment variables.
	defaultSkydHost = "sia"

	// defaultSkydPort is where we connect to skyd unless overwritten by
	// "API_PORT" environment variables.
	defaultSkydPort = 9980
)

func main() {
	// Load the environment variables from the .env file.
	// Existing variables take precedence and won't be overwritten.
	_ = godotenv.Load()

	// Initialise the global context and logger. These will be used throughout
	// the service. Once the context is closed, all background threads will
	// wind themselves down.
	ctx := context.Background()
	logger := logrus.New()
	logLevel, err := logrus.ParseLevel(os.Getenv("BLOCKER_LOG_LEVEL"))
	if err != nil {
		logLevel = logrus.InfoLevel
	}
	logger.SetLevel(logLevel)

	// Set the unique id of this server.
	database.ServerUID = os.Getenv("SERVER_UID")
	if database.ServerUID == "" {
		log.Fatal("missing env var SERVER_UID")
	}

	// Initialised the database connection.
	uri, dbCreds, err := loadDBCredentials()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to fetch db credentials"))
	}
	db, err := database.New(ctx, uri, dbCreds, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to connect to the db"))
	}

	// Blocker env vars.
	skydPort := defaultSkydPort
	skydPortEnv, err := strconv.Atoi(os.Getenv("API_PORT"))
	if err == nil && skydPortEnv > 0 {
		skydPort = skydPortEnv
	}
	skydHost := defaultSkydHost
	if skydHostEnv := os.Getenv("API_HOST"); skydHostEnv != "" {
		skydHost = skydHostEnv
	}
	skydAPIPassword := os.Getenv("SIA_API_PASSWORD")
	if skydAPIPassword == "" {
		log.Fatal(errors.New("SIA_API_PASSWORD is empty, exiting"))
	}

	// Accounts.
	if aHost := os.Getenv("SKYNET_ACCOUNTS_HOST"); aHost != "" {
		api.AccountsHost = aHost
	}
	if aPort := os.Getenv("SKYNET_ACCOUNTS_PORT"); aPort != "" {
		api.AccountsPort = aPort
	}

	// Create a skyd API.
	skydAPI, err := skyd.NewAPI(skydHost, skydAPIPassword, skydPort, db, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to instantiate Skyd API"))
	}
	if !skydAPI.IsSkydUp() {
		log.Fatal(errors.New("skyd down, exiting"))
	}

	// Create the blocker.
	bl, err := blocker.New(ctx, skydAPI, db, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to instantiate blocker"))
	}

	// Start blocker.
	err = bl.Start()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to start blocker"))
	}

	// Create the syncer.
	portalURLs := loadPortalURLs()
	sync, err := syncer.New(ctx, db, portalURLs, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to instantiate syncer"))
	}

	// Start the syncer.
	err = sync.Start()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to start syncer"))
	}

	// Initialise the server.
	server, err := api.New(skydAPI, db, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to build the api"))
	}

	// TODO: Missing clean shutdown and database disconnect.
	log.Fatal(server.ListenAndServe(4000))
}

// loadDBCredentials creates a new db connection based on credentials found in
// the environment variables.
func loadDBCredentials() (string, options.Credential, error) {
	var creds options.Credential
	var ok bool
	if creds.Username, ok = os.LookupEnv("SKYNET_DB_USER"); !ok {
		return "", options.Credential{}, errors.New("missing env var SKYNET_DB_USER")
	}
	if creds.Password, ok = os.LookupEnv("SKYNET_DB_PASS"); !ok {
		return "", options.Credential{}, errors.New("missing env var SKYNET_DB_PASS")
	}
	var host, port string
	if host, ok = os.LookupEnv("SKYNET_DB_HOST"); !ok {
		return "", options.Credential{}, errors.New("missing env var SKYNET_DB_HOST")
	}
	if port, ok = os.LookupEnv("SKYNET_DB_PORT"); !ok {
		return "", options.Credential{}, errors.New("missing env var SKYNET_DB_PORT")
	}
	return fmt.Sprintf("mongodb://%v:%v", host, port), creds, nil
}

// loadPortalURLs returns a slice of portal urls, configured in the environment
// under the key BLOCKER_SYNC_PORTALS. The blocker will keep in sync the
// blocklist from these portals with the local skyd instance.
func loadPortalURLs() (portalURLs []string) {
	portalURLStr := os.Getenv("BLOCKER_PORTALS_SYNC")
	for _, portalURL := range strings.Split(portalURLStr, ",") {
		portalURL = sanitizePortalURL(portalURL)
		if portalURL != "" {
			portalURLs = append(portalURLs, portalURL)
		}
	}
	return
}

// sanitizePortalURL is a helper function that sanitizes the given input portal
// URL, stripping away trailing slashes and ensuring it's prefixed with https.
func sanitizePortalURL(portalURL string) string {
	portalURL = strings.TrimSpace(portalURL)
	portalURL = strings.TrimSuffix(portalURL, "/")
	if strings.HasPrefix(portalURL, "https://") {
		return portalURL
	}
	portalURL = strings.TrimPrefix(portalURL, "http://")
	if portalURL == "" {
		return portalURL
	}
	return fmt.Sprintf("https://%s", portalURL)
}
