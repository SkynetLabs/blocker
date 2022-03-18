package api

import (
	"fmt"
	"net/http"

	"github.com/SkynetLabs/blocker/database"
	"github.com/julienschmidt/httprouter"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
)

// API is our central entry point to all subsystems relevant to serving
// requests.
type API struct {
	staticDB         *database.DB
	staticLogger     *logrus.Logger
	staticRouter     *httprouter.Router
	staticSkydClient *Client
}

// New creates a new API instance.
func New(skydClient *Client, db *database.DB, logger *logrus.Logger) (*API, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}
	if skydClient == nil {
		return nil, errors.New("no skyd client provided")
	}
	router := httprouter.New()
	router.RedirectTrailingSlash = true

	api := &API{
		staticDB:         db,
		staticLogger:     logger,
		staticRouter:     router,
		staticSkydClient: skydClient,
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
