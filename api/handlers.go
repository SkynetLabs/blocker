package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/julienschmidt/httprouter"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

type (
	// BlockPOST describes a request to the /block endpoint.
	BlockPOST struct {
		Skylink  skylink  `json:"skylink"`
		Reporter Reporter `json:"reporter"`
		Tags     []string `json:"tags"`
	}

	// BlockWithPoWPOST describes a request to the /blockpow endpoint
	// containing a pow.
	BlockWithPoWPOST struct {
		BlockPOST
		PoW blocker.BlockPoW `json:"pow"`
	}

	// BlockWithPoWGET is the response a user gets from the /blockpow
	// endpoint.
	BlockWithPoWGET struct {
		Target string `json:"target"`
	}

	// Reporter is a person who reported that a given skylink should be
	// blocked.
	Reporter struct {
		Name         string `json:"name"`
		Email        string `json:"email"`
		OtherContact string `json:"othercontact"`
	}

	// statusResponse is what we return on block requests
	statusResponse struct {
		Status string `json:"status"`
	}

	// skylink is a helper type which adds custom decoding for skylinks.
	skylink string
)

// UnmarshalJSON implements json.Unmarshaler for a skylink.
func (sl *skylink) UnmarshalJSON(b []byte) error {
	var link string
	err := json.Unmarshal(b, &link)
	if err != nil {
		return err
	}
	// Trim all the redundant information.
	link, err = extractSkylinkHash(link)
	if err != nil {
		return err
	}
	// Normalise the skylink hash. We want to use the same hash encoding in the
	// database, regardless of the encoding of the skylink when we receive it -
	// base32 or base64.
	var slNormalized skymodules.Skylink
	err = slNormalized.LoadString(link)
	if err != nil {
		return errors.AddContext(err, "invalid skylink provided")
	}
	*sl = skylink(slNormalized.String())
	return nil
}

// healthGET returns the status of the service
func (api *API) healthGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	status := struct {
		DBAlive bool `json:"dbAlive"`
	}{}

	// Apply a timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	err := api.staticDB.Ping(ctx)
	status.DBAlive = err == nil
	skyapi.WriteJSON(w, status)
}

// blockWithPoWPOST blocks a skylink. It is meant to be used by untrusted
// sources such as the abuse report skapp. The PoW prevents users from easily
// and anonymously blocking large numbers of skylinks. Instead it encourages
// reuse of proofs which improves the linkability between reports, thus allowing
// us to more easily unblock a batch of links.
func (api *API) blockWithPoWPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	// Protect against large bodies.
	b := http.MaxBytesReader(w, r.Body, 1<<16) // 64 kib
	defer b.Close()

	// Parse the request.
	var body BlockWithPoWPOST
	err := json.NewDecoder(b).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Verify the pow.
	err = body.PoW.Verify()
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}

	// Use the MySkyID as the suband make sure we don't consider the
	// reporter authenticated.
	sub := hex.EncodeToString(body.PoW.MySkyID[:])

	// Check whether the skylink is on the allow list
	if api.isAllowListed(r.Context(), string(body.Skylink)) {
		skyapi.WriteJSON(w, statusResponse{"reported"})
		return
	}

	// Block the link.
	err = api.block(r.Context(), body.BlockPOST, sub, true)
	if errors.Contains(err, database.ErrSkylinkExists) {
		skyapi.WriteJSON(w, statusResponse{"duplicate"})
		return
	}
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}
	skyapi.WriteJSON(w, statusResponse{"reported"})
}

// blockWithPoWGET is the handler for the /blockpow [GET] endpoint.
func (api *API) blockWithPoWGET(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	skyapi.WriteJSON(w, BlockWithPoWGET{
		Target: hex.EncodeToString(blocker.MySkyTarget[:]),
	})
}

// blockPOST blocks a skylink. It is meant to be used by trusted sources such as
// the malware scanner or abuse email scanner.
func (api *API) blockPOST(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	var body BlockPOST
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusBadRequest)
		return
	}
	sub := r.Form.Get("sub")
	if sub == "" {
		// No sub. Maybe we didn't try to fetch it? Try now. Don't log errors.
		u, err := UserFromReq(r, api.staticLogger)
		if err == nil {
			sub = u.Sub
		}
	}

	// Check whether the skylink is on the allow list
	if api.isAllowListed(r.Context(), string(body.Skylink)) {
		skyapi.WriteJSON(w, statusResponse{"reported"})
		return
	}

	// Block the link.
	err = api.block(r.Context(), body, sub, sub == "")
	if errors.Contains(err, database.ErrSkylinkExists) {
		skyapi.WriteJSON(w, statusResponse{"duplicate"})
		return
	}
	if err != nil {
		skyapi.WriteError(w, skyapi.Error{err.Error()}, http.StatusInternalServerError)
		return
	}
	skyapi.WriteJSON(w, statusResponse{"reported"})
}

// block blocks a skylink
func (api *API) block(ctx context.Context, bp BlockPOST, sub string, unauthenticated bool) error {
	skylink := &database.BlockedSkylink{
		Skylink: string(bp.Skylink),
		Reporter: database.Reporter{
			Name:            bp.Reporter.Name,
			Email:           bp.Reporter.Email,
			OtherContact:    bp.Reporter.OtherContact,
			Sub:             sub,
			Unauthenticated: unauthenticated,
		},
		Tags:           bp.Tags,
		TimestampAdded: time.Now().UTC(),
	}
	api.staticLogger.Tracef("blockPOST will block skylink %s", skylink.Skylink)
	err := api.staticDB.CreateBlockedSkylink(ctx, skylink)
	if err != nil {
		return err
	}
	api.staticLogger.Debugf("Added skylink %s", skylink.Skylink)
	return nil
}

// isAllowListed returns true if the given skylink is on the allow list
func (api *API) isAllowListed(ctx context.Context, skylink string) bool {
	// try and resolve the skylink
	skylink, err := api.staticSkydAPI.ResolveSkylink(skylink)
	if err != nil {
		api.staticLogger.Error("failed to resolve skylink", err)
	}

	// check whether the skylink is allow listed
	allowlisted, err := api.staticDB.IsAllowListed(ctx, skylink)
	if err != nil {
		api.staticLogger.Error("failed to verify skylink against the allow list", err)
		return false
	}
	return allowlisted
}

// extractSkylinkHash extracts the skylink hash from the given skylink that
// might have protocol, path, etc. within it.
func extractSkylinkHash(skylink string) (string, error) {
	extractSkylinkRE := regexp.MustCompile("^.*([a-z0-9]{55})|([a-zA-Z0-9-_]{46}).*$")
	m := extractSkylinkRE.FindStringSubmatch(skylink)
	if len(m) < 3 || (m[1] == "" && m[2] == "") {
		return "", errors.New("no valid skylink found in string " + skylink)
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}
