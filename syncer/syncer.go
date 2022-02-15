package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
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
			Testing:  time.Second,
			Standard: 4 * time.Hour,
		},
	).(time.Duration)
)

type (
	// Syncer periodically scans the blocklist of a set of portals which are
	// configured by the user and adds the missing hashes to the blocklist of
	// the local skyd. Alongside syncing with other portal's blocklists, the
	// syncer will also ensure the local skyd's blocklist has all of the links
	// which are contained in the database this syncer is connected to, ensuring
	// all hashes are on its blocklist.
	Syncer struct {
		staticCtx        context.Context
		staticDB         *database.DB
		staticBlocker    *blocker.Blocker
		staticLogger     *logrus.Logger
		staticPortalURLs []string
		staticSkydAPI    skyd.API
	}

	// lookupTable is a helper struct that defines a hash map
	lookupTable map[crypto.Hash]struct{}
)

// New returns a new Syncer with the given parameters.
func New(ctx context.Context, blocker *blocker.Blocker, skydAPI skyd.API, db *database.DB, portalURLs []string, logger *logrus.Logger) (*Syncer, error) {
	if ctx == nil {
		return nil, errors.New("invalid context provided")
	}
	if db == nil {
		return nil, errors.New("invalid DB provided")
	}
	if logger == nil {
		return nil, errors.New("invalid logger provided")
	}
	if skydAPI == nil {
		return nil, errors.New("invalid Skyd API provided")
	}
	s := &Syncer{
		staticBlocker:    blocker,
		staticCtx:        ctx,
		staticDB:         db,
		staticLogger:     logger,
		staticPortalURLs: portalURLs,
		staticSkydAPI:    skydAPI,
	}
	return s, nil
}

// Start launches a background task that periodically syncs the blocklists of
// the preconfigured portals with the blocklist of the local skyd instance.
func (s *Syncer) Start() {
	// convenience variables
	logger := s.staticLogger

	// sync the local mongo database with skyd
	err := s.syncDatabaseWithSkyd()
	if err != nil {
		logger.Errorf("Local sync failed with error: %s", err.Error())
	}

	// start the sync loop.
	go func() {
		for {
			// build a lookup table of existing hashes
			existing, err := s.buildLookupTable()
			if err == nil {
				for _, portalURL := range s.staticPortalURLs {
					err := s.syncPortalWithSkyd(portalURL, existing)
					if err != nil {
						logger.Errorf("Sync with %v failed with error: %s", portalURL, err)
					}
				}
			} else {
				logger.Errorf("could not build lookup table of existing hashes, error: %v", err)
			}

			select {
			case <-s.staticCtx.Done():
				return
			case <-time.After(syncInterval):
			}
		}
	}()
}

// blocklistFromPortal returns the blocklist for the portal at the given URL.
func (s *Syncer) blocklistFromPortal(portalURL string) ([]crypto.Hash, error) {
	// convenience variables
	logger := s.staticLogger

	// build the request to fetch the blocklist from the portal
	url := fmt.Sprintf("%s/skynet/blocklist", portalURL)
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

// buildLookupTable is a small helper function that fetches the blocklist from
// the skyd API and turns it into a lookup table that can be used to see whether
// a hash is part of the blocklist.
func (s *Syncer) buildLookupTable() (lookupTable, error) {
	blocklist, err := s.staticSkydAPI.Blocklist()
	if err != nil {
		return nil, err
	}
	lookup := make(map[crypto.Hash]struct{})
	for _, hash := range blocklist {
		lookup[hash] = struct{}{}
	}
	return lookup, nil
}

// syncDatabaseWithSkyd will diff the blocklist of the connected skyd with the
// list of hashes in the database and add any missing hashes
func (s *Syncer) syncDatabaseWithSkyd() error {
	// convenience variables
	logger := s.staticLogger

	// build a lookup table of existing hashes
	existing, err := s.buildLookupTable()
	if err != nil {
		return errors.AddContext(err, "could not build lookup table of existing hashes")
	}

	// fetch all hashes from our database
	hashes, err := s.staticDB.Hashes()
	if err != nil {
		return errors.AddContext(err, "could not get hashes from database")
	}

	// filter out all hashes which are already blocked by skyd
	var curr int
	for _, hash := range hashes {
		if _, exists := existing[hash.Hash]; !exists {
			hashes[curr] = hash
			curr++
		}
	}
	hashes = hashes[:curr]

	// use the blocker (has batching built in) to block the ones we are missing
	added, _, err := s.staticBlocker.BlockHashes(hashes)
	if err != nil {
		return errors.AddContext(err, "could not block hashes")
	}

	logger.Infof("successfully synced hashes in database with skyd, %v hashes added", added)
	return nil
}

// sync will loop through the preconfigured portals and pull their blocklist and
// ensure all of the hashes on those blocklists are blocked in the local skyd
// instance.
func (s *Syncer) syncPortalWithSkyd(portalURL string, existing lookupTable) error {
	// convenience variables
	logger := s.staticLogger

	logger.Infof("syncing blocklist for portal '%s'", portalURL)

	// fetch the blocklist from the portal
	hashes, err := s.blocklistFromPortal(portalURL)
	if err != nil {
		return errors.AddContext(err, fmt.Sprintf("failed getting blocklist from portal %s", portalURL))
	}

	// filter out all hashes which are already blocked by skyd
	var toAdd []database.Hash
	for _, hash := range hashes {
		if _, exists := existing[hash]; !exists {
			toAdd = append(toAdd, database.Hash{Hash: hash})
		}
	}

	added, _, err := s.staticBlocker.BlockHashes(toAdd)
	if err != nil {
		return errors.AddContext(err, "failed to block hashes")
	}

	logger.Infof("added %v hashes from portal '%s'", added, portalURL)
	return nil
}
