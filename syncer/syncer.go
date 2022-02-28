package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/build"
	"go.sia.tech/siad/crypto"
)

var (
	// syncInterval defines the amount of time between syncs of external
	// portal's blocklists, which can be defined in the environment using the
	// key BLOCKER_SYNC_LIST
	syncInterval = build.Select(
		build.Var{
			Dev:      time.Minute,
			Testing:  time.Minute,
			Standard: 4 * time.Hour,
		},
	).(time.Duration)
)

type (
	// Syncer periodically fetches the latest blocklist additions from a
	// configured set of portals, adding them the local blocklist database.
	Syncer struct {
		started bool

		// lastSyncedHash is a map that keeps track of the last synced hash per
		// portal URL, when that hash is encountered in consecutive calls to
		// fetch that portal's blocklist, we know we can stop paging
		lastSyncedHash map[string]string

		staticPortalURLs []string

		staticDB      *database.DB
		staticBlocker *blocker.Blocker
		staticLogger  *logrus.Logger

		staticCtx context.Context
		staticMu  sync.Mutex
	}
)

// New returns a new Syncer with the given parameters.
func New(ctx context.Context, blocker *blocker.Blocker, db *database.DB, portalURLs []string, logger *logrus.Logger) (*Syncer, error) {
	if ctx == nil {
		return nil, errors.New("no context provided")
	}
	if blocker == nil {
		return nil, errors.New("no blocker provided")
	}
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}
	s := &Syncer{
		staticBlocker:    blocker,
		staticCtx:        ctx,
		staticDB:         db,
		staticLogger:     logger,
		staticPortalURLs: portalURLs,
	}
	return s, nil
}

// Start launches a background task that periodically syncs the blocklists of
// the preconfigured portals with the blocklist of the local skyd instance.
func (s *Syncer) Start() error {
	s.staticMu.Lock()
	defer s.staticMu.Unlock()

	// convenience variables
	logger := s.staticLogger

	// escape early if the syncer has no portal urls configured
	if len(s.staticPortalURLs) == 0 {
		logger.Infof("syncer is not being started because no portal URLs have been defined")
		return nil
	}

	// assert 'Start' is only called once
	if s.started {
		return errors.New("syncer already started")
	}
	s.started = true

	// start the sync loop
	go s.threadedSyncLoop()

	return nil
}

// threadedSyncLoop holds the main sync loop
func (s *Syncer) threadedSyncLoop() {
	// convenience variables
	logger := s.staticLogger

	for {
		err := s.syncPortals()
		if err != nil {
			logger.Errorf("failed to sync portals with skyd, error %v", err)
		}

		select {
		case <-s.staticCtx.Done():
			return
		case <-time.After(syncInterval):
		}
	}
}

// blocklistFromPortal returns the blocklist for the portal at the given URL.
func (s *Syncer) blocklistFromPortal(portalURL string, offset, limit int) (api.BlocklistGET, error) {
	// convenience variables
	logger := s.staticLogger

	// prepare query string params
	values := url.Values{}
	values.Set("offset", fmt.Sprint(offset))
	values.Set("limit", fmt.Sprint(limit))
	queryString := values.Encode()

	// build the request to fetch the blocklist from the portal
	url := fmt.Sprintf("%s/skynet/portal/blocklist?%s", portalURL, queryString)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.AddContext(err, "failed to build blocklist request")
	}

	// set headers and execute the request
	req.Header.Set("User-Agent", "Sia-Agent")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.AddContext(err, "failed to fetch blocklist")
	}
	defer func() {
		err = resp.Body.Close()
		if err != nil {
			logger.Errorf("failed to close the request body, err: %v", err)
		}
	}()

	// decode the response
	br := struct{ Blocklist []crypto.Hash }{}
	err = json.NewDecoder(resp.Body).Decode(&br)
	if err != nil {
		return nil, errors.AddContext(err, "failed to decode skyd API response")
	}

	return br.Blocklist, nil
}

// syncPortalsWithSkyd will sync the blocklist of all portals defined on the
// syncer with the local skyd.
func (s *Syncer) syncPortals() error {
	// convenience variables
	logger := s.staticLogger

	// sync all portals one by one
	var errs []error
	for _, portalURL := range s.staticPortalURLs {
		logger.Infof("syncing blocklist for portal '%s'", portalURL)

		// fetch the blocklist from the portal
		portalBlocklist, err := s.blocklistFromPortal(portalURL)
		if err != nil {
			return errors.AddContext(err, fmt.Sprintf("could not fetch blocklist from portal %s", portalURL))
		}

		// sync the blocklist with skyd
		update := buildLookupTable(portalBlocklist)
		added, err := s.syncBlocklistWithSkyd(existing, update)
		if err != nil {
			errs = append(errs, errors.AddContext(err, fmt.Sprintf("sync with portal '%v' failed", portalURL)))
		}

		logger.Infof("added %v hashes from portal '%s'", added, portalURL)
	}

	return errors.Compose(errs...)
}

// syncBlocklistWithSkyd takes two lookup tables, one containing a mapping of
// the hashes that currently exist in skyd, and one with the hashes of the
// portal with which we are syncing. The diff is added to skyd.
func (s *Syncer) syncBlocklistWithSkyd(existing, update lookupTable) (int, error) {
	// filter out all hashes which are already blocked by skyd
	var toAdd []database.Hash
	for hash := range update {
		if _, exists := existing[hash]; !exists {
			toAdd = append(toAdd, database.Hash{Hash: hash})
		}
	}

	// block the hashes
	blocked, _, err := s.staticBlocker.BlockHashes(toAdd)
	if err != nil {
		return 0, errors.AddContext(err, "failed to block hashes")
	}

	return blocked, nil
}
