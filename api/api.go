package api

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/SkynetLabs/blocker/database"
	"github.com/julienschmidt/httprouter"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
)

var (
	// SkydHost is where we connect to skyd
	SkydHost = "sia"
	// SkydPort is where we connect to skyd
	SkydPort = 9980
	// SkydAPIPassword is the API password for skyd
	SkydAPIPassword string
)

// API is our central entry point to all subsystems relevant to serving requests.
type API struct {
	staticDB     *database.DB
	staticRouter *httprouter.Router
	staticLogger *logrus.Logger
}

// New creates a new API instance.
func New(db *database.DB, logger *logrus.Logger) (*API, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}
	router := httprouter.New()
	router.RedirectTrailingSlash = true

	api := &API{
		staticDB:     db,
		staticRouter: router,
		staticLogger: logger,
	}

	api.buildHTTPRoutes()
	return api, nil
}

// ListenAndServe starts the API server on the given port.
func (api *API) ListenAndServe(port int) error {
	api.staticLogger.Info(fmt.Sprintf("Listening on port %d", port))
	return http.ListenAndServe(fmt.Sprintf(":%d", port), api.staticRouter)
}

// ServeHTTP implements the http.Handler interface.
func (api *API) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	api.staticRouter.ServeHTTP(w, req)
}

// AuthHeader returns the value we need to set to the `Authorization` header in
// order to call `skyd`.
func AuthHeader() string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(":"+SkydAPIPassword)))
}
