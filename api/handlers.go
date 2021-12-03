package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/SkynetLabs/blocker/database"
	accdb "github.com/SkynetLabs/skynet-accounts/database"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// BlockPOST ...
	BlockPOST struct {
		Skylink  string            `json:"skylink"`
		Reporter database.Reporter `json:"reporter"`
		Tags     []string          `json:"tags"`
	}
)

// healthGET returns the status of the service
func (api *API) healthGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	status := struct {
		DBAlive bool `json:"dbAlive"`
	}{}
	err := api.staticDB.Ping(r.Context())
	status.DBAlive = err == nil
	skyapi.WriteJSON(w, status)
}

// blockPOST blocks a skylink
func (api *API) blockPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var body BlockPOST
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}
	body.Skylink, err = accdb.ExtractSkylinkHash(body.Skylink)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{errors.AddContext(err, "invalid skylink provided").Error()}, http.StatusBadRequest)
		return
	}
	// Normalise the skylink hash. We want to use the same hash encoding in the
	// database, regardless of the encoding of the skylink when we receive it -
	// base32 or base64.
	var sl skymodules.Skylink
	err = sl.LoadString(body.Skylink)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{errors.AddContext(err, "invalid skylink provided").Error()}, http.StatusBadRequest)
		return
	}
	body.Skylink = sl.String()
	skylink := &database.BlockedSkylink{
		Skylink:        body.Skylink,
		Reporter:       body.Reporter,
		Tags:           body.Tags,
		TimestampAdded: time.Now().UTC(),
	}
	skylink.Reporter.Sub = r.Form.Get("sub")
	api.staticLogger.Tracef("blockPOST will block skylink %s", skylink)
	err = api.staticDB.BlockedSkylinkCreate(r.Context(), skylink)
	if errors.Contains(err, database.ErrSkylinkExists) {
		skyapi.WriteJSON(w, "BlockedSkylink already exists in the database")
		return
	}
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}
	api.staticLogger.Debugf("Added skylink %s", skylink.Skylink)
	skyapi.WriteSuccess(w)
}
