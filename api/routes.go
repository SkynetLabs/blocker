package api

import (
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	api2 "gitlab.com/SkynetLabs/skyd/node/api"
)

var (
	// AccountsHost is the host on which the accounts service is listening.
	AccountsHost = "accounts"
	// AccountsPort is the port on which the accounts service is listening.
	AccountsPort = "3000"
)

// buildHTTPRoutes registers all HTTP routes and their handlers.
func (api *API) buildHTTPRoutes() {
	api.staticRouter.GET("/health", api.healthGET)
	api.staticRouter.POST("/block", api.validateCookie(api.blockPOST))
}

// validateCookie extracts the cookie from the incoming blocking request and
// uses it to get user info from accounts. This action utilises accounts'
// infrastructure to validate the cookie.
func (api *API) validateCookie(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		cookie, err := req.Cookie("skynet-jwt")
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Access-Control-Allow-Origin", "https://0404guluqu38oaqapku91ed11kbhkge55smh9lhjukmlrj37lfpm8no.siasky.dev")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,access-control-allow-origin, access-control-allow-headers")
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			err = errors.AddContext(err, "failed to read skynet cookie")
			api2.WriteError(w, api2.Error{err.Error()}, http.StatusUnauthorized)
			return
		}
		url := fmt.Sprintf("http://%s:%s/user", AccountsHost, AccountsPort)
		areq, err := http.NewRequest(http.MethodGet, url, nil)
		areq.AddCookie(cookie)
		aresp, err := http.DefaultClient.Do(areq)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Access-Control-Allow-Origin", "https://0404guluqu38oaqapku91ed11kbhkge55smh9lhjukmlrj37lfpm8no.siasky.dev")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,access-control-allow-origin, access-control-allow-headers")
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			err = errors.AddContext(err, "validateCookie: failed to talk to accounts")
			api2.WriteError(w, api2.Error{err.Error()}, http.StatusUnauthorized)
			return
		}
		defer aresp.Body.Close()
		if aresp.StatusCode != http.StatusOK {
			b, _ := ioutil.ReadAll(aresp.Body)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Access-Control-Allow-Origin", "https://0404guluqu38oaqapku91ed11kbhkge55smh9lhjukmlrj37lfpm8no.siasky.dev")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,access-control-allow-origin, access-control-allow-headers")
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			api.staticLogger.Tracef("validateCookie: failed to talk to accounts, status code %d, body %s", aresp.StatusCode, string(b))
			api2.WriteError(w, api2.Error{"Unauthorized"}, http.StatusUnauthorized)
			return
		}

		h(w, req, ps)
	}
}
