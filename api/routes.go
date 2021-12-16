package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	url "net/url"

	"github.com/SkynetLabs/skynet-accounts/database"
	"github.com/julienschmidt/httprouter"
	"github.com/sirupsen/logrus"
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
	api.staticRouter.POST("/block", api.blockPOST)
	api.staticRouter.POST("/powblock", api.addCORSHeader(api.blockWithPoWPOST))
}

// addCORSHeader sets the CORS headers.
func (api *API) addCORSHeader(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "https://0404guluqu38oaqapku91ed11kbhkge55smh9lhjukmlrj37lfpm8no.siasky.net")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,access-control-allow-origin, access-control-allow-headers")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		h(w, req, ps)
	}
}

// validateCookie extracts the cookie from the incoming blocking request and
// uses it to get user info from accounts. This action utilises accounts'
// infrastructure to validate the cookie.
func (api *API) validateCookie(h httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		u, err := UserFromReq(req, api.staticLogger)
		if err != nil {
			api2.WriteError(w, api2.Error{err.Error()}, http.StatusUnauthorized)
			return
		}
		if req.Form == nil {
			req.Form = url.Values{}
		}
		req.Form.Set("sub", u.Sub)

		h(w, req, ps)
	}
}

// UserFromReq identifies the user making the request by reading the attached
// skynet cookie and querying Accounts service for the user's info.
func UserFromReq(req *http.Request, logger *logrus.Logger) (*database.User, error) {
	cookie, err := req.Cookie("skynet-jwt")
	if err != nil {
		return nil, errors.AddContext(err, "failed to read skynet cookie")
	}
	accountsURL := fmt.Sprintf("http://%s:%s/user", AccountsHost, AccountsPort)
	areq, err := http.NewRequest(http.MethodGet, accountsURL, nil)
	areq.AddCookie(cookie)
	aresp, err := http.DefaultClient.Do(areq)
	if err != nil {
		return nil, errors.AddContext(err, "validateCookie: failed to talk to accounts")
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(aresp.Body)
		logger.Tracef("validateCookie: failed to talk to accounts, status code %d, body %s", aresp.StatusCode, string(b))
		return nil, errors.New("Unauthorized")
	}
	var u database.User
	err = json.NewDecoder(aresp.Body).Decode(&u)
	if err != nil {
		logger.Warnf("validateCookie: failed to parse accounts' response body: %s", err.Error())
		return nil, err
	}
	return &u, nil
}
