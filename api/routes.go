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
	api.staticRouter.GET("/skylinks", api.validateAuth(api.skylinksGET, false))
	api.staticRouter.POST("/block", api.addCORSHeader(api.validateAuth(api.blockPOST, true)))
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

// validateAuth authenticates the requests. A request can be authenticated in
// two ways, using basic auth or cookies.
//
// In case of HTTP basic auth it will validate the given password.
//
// In case of cookie auth it will  extract the cookie from the incoming blocking
// request and uses it to get user info from accounts. This action utilises
// accounts' infrastructure to validate the cookie.
func (api *API) validateAuth(h httprouter.Handle, allowCookie bool) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
		// check basic auth
		_, pass, ok := req.BasicAuth()
		if ok && pass == api.staticAPIPassword {
			h(w, req, ps)
			return
		}

		// check whether cookie auth is allowed
		if !allowCookie {
			api2.WriteError(w, api2.Error{"No basic auth found and cookie auth not allowed"}, http.StatusUnauthorized)
			return
		}
		// check cookie auth
		user, err := cookieAuth(req, api.staticLogger)
		if err != nil {
			api2.WriteError(w, api2.Error{err.Error()}, http.StatusUnauthorized)
			return
		}

		// add the user info to the form
		if req.Form == nil {
			req.Form = url.Values{}
		}
		req.Form.Set("sub", user.Sub)

		h(w, req, ps)
	}
}

// cookieAuth authenticates the request using a cookie
//
// TODO: this function was extracted and copy pasted, should be cleaned up
func cookieAuth(req *http.Request, logger *logrus.Logger) (*database.User, error) {
	cookie, err := req.Cookie("skynet-jwt")
	if err != nil {
		return nil, errors.AddContext(err, "failed to read skynet cookie")
	}

	// call out to accounts API
	accountsURL := fmt.Sprintf("http://%s:%s/user", AccountsHost, AccountsPort)
	areq, err := http.NewRequest(http.MethodGet, accountsURL, nil)
	areq.AddCookie(cookie)
	aresp, err := http.DefaultClient.Do(areq)
	if err != nil {
		return nil, errors.AddContext(err, "validateCookie: failed to talk to accounts")
	}
	defer aresp.Body.Close()

	// check status code
	if aresp.StatusCode != http.StatusOK {
		b, _ := ioutil.ReadAll(aresp.Body)
		logger.Tracef("validateCookie: failed to talk to accounts, status code %d, body %s", aresp.StatusCode, string(b))
		return nil, errors.New("validateCookie: unexpected status code from  accounts API")
	}

	// fetch database user
	var u database.User
	err = json.NewDecoder(aresp.Body).Decode(&u)
	if err != nil {
		logger.Warnf("validateCookie: failed to parse accounts' response body: %s", err.Error())
		return nil, errors.AddContext(err, "validateCookie: faild to parse accounts response")
	}

	return &u, nil
}
