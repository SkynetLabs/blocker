package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	accdb "github.com/SkynetLabs/skynet-accounts/database"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
)

// loadDBCredentials creates a new db connection based on credentials found in
// the environment variables.
func loadDBCredentials() (accdb.DBCredentials, error) {
	var cds accdb.DBCredentials
	var ok bool
	if cds.User, ok = os.LookupEnv("SKYNET_DB_USER"); !ok {
		return accdb.DBCredentials{}, errors.New("missing env var SKYNET_DB_USER")
	}
	if cds.Password, ok = os.LookupEnv("SKYNET_DB_PASS"); !ok {
		return accdb.DBCredentials{}, errors.New("missing env var SKYNET_DB_PASS")
	}
	if cds.Host, ok = os.LookupEnv("SKYNET_DB_HOST"); !ok {
		return accdb.DBCredentials{}, errors.New("missing env var SKYNET_DB_HOST")
	}
	if cds.Port, ok = os.LookupEnv("SKYNET_DB_PORT"); !ok {
		return accdb.DBCredentials{}, errors.New("missing env var SKYNET_DB_PORT")
	}
	return cds, nil
}

// isSkydUp connects to the local skyd and checks its status.
// Returns true only if skyd is fully ready.
func isSkydUp(logger *logrus.Logger) bool {
	status := struct {
		Ready     bool
		Consensus bool
		Gateway   bool
		Renter    bool
	}{}
	url := fmt.Sprintf("http://%s:%d/daemon/ready", api.SkydHost, api.SkydPort)
	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		logger.Fatal(err)
		return false
	}
	r.Header.Set("User-Agent", "Sia-Agent")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		logger.Warnf("Failed to query skyd: %s", err.Error())
		return false
	}
	defer resp.Body.Close()
	err = json.NewDecoder(resp.Body).Decode(&status)
	if err != nil {
		logger.Warnf("Bad body from skyd's /daemon/ready: %s", err.Error())
		return false
	}
	return status.Ready && status.Consensus && status.Gateway && status.Renter
}

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

	// Set the preferred portal address.
	database.Portal = os.Getenv("PORTAL_DOMAIN")
	if database.Portal == "" {
		log.Fatal("missing env var PORTAL_DOMAIN")
	}
	if !strings.HasPrefix(database.Portal, "http") {
		database.Portal = "https://" + database.Portal
	}
	// Set the unique name of this server.
	database.ServerDomain = os.Getenv("SERVER_DOMAIN")
	if database.ServerDomain == "" {
		log.Fatal("missing env var SERVER_DOMAIN")
	}

	// Initialised the database connection.
	dbCreds, err := loadDBCredentials()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to fetch db credentials"))
	}
	db, err := database.New(ctx, dbCreds, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to connect to the db"))
	}

	// Connect to skyd.
	skydPort, err := strconv.Atoi(os.Getenv("API_PORT"))
	if err == nil && skydPort > 0 {
		api.SkydPort = skydPort
	}
	if skydHost := os.Getenv("API_HOST"); skydHost != "" {
		api.SkydHost = skydHost
	}
	if !isSkydUp(logger) {
		log.Fatal(errors.New("skyd down, exiting"))
	}

	api.SkydAPIPassword = os.Getenv("SIA_API_PASSWORD")
	if api.SkydAPIPassword == "" {
		log.Fatal(errors.New("SIA_API_PASSWORD is empty, exiting"))
	}

	// Accounts.
	if aHost := os.Getenv("SKYNET_ACCOUNTS_HOST"); aHost != "" {
		api.AccountsHost = aHost
	}
	if aPort := os.Getenv("SKYNET_ACCOUNTS_PORT"); aPort != "" {
		api.AccountsPort = aPort
	}

	// Initialise and start the background scanner task.
	if nginxList := os.Getenv("BLOCKER_NGINX_CACHE_PURGE_LIST"); nginxList != "" {
		blocker.NginxCachePurgerListPath = nginxList
	}
	if nginxLock := os.Getenv("BLOCKER_NGINX_CACHE_PURGE_LOCK"); nginxLock != "" {
		blocker.NginxCachePurgeLockPath = nginxLock
	}
	blockerThread, err := blocker.New(ctx, db, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to instantiate blocker"))
	}
	blockerThread.Start()

	// Initialise the server.
	server, err := api.New(db, logger)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to build the api"))
	}

	log.Fatal(server.ListenAndServe(4000))
}
