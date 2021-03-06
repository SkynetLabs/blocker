package syncer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/database"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.sia.tech/siad/build"
)

const (
	// stopTimeoutDuration is the amount of time we wait when stop is called
	// before cancelling out and returning with an error indicating an unclean
	// shutdown.
	stopTimeoutDuration = time.Minute
)

var (
	// syncInterval defines the amount of time between syncs of external
	// portal's blocklists, which can be defined in the environment using the
	// key BLOCKER_SYNC_LIST
	syncInterval = build.Select(
		build.Var{
			Dev:      time.Minute,
			Testing:  time.Minute,
			Standard: 15 * time.Minute,
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

		staticDB         *database.DB
		staticLogger     *logrus.Logger
		staticMu         sync.Mutex
		staticPortalURLs []string

		staticStopChan  chan struct{}
		staticWaitGroup sync.WaitGroup
	}
)

// New returns a new Syncer with the given parameters.
func New(db *database.DB, portalURLs []string, logger *logrus.Logger) (*Syncer, error) {
	if db == nil {
		return nil, errors.New("no DB provided")
	}
	if logger == nil {
		return nil, errors.New("no logger provided")
	}
	s := &Syncer{
		lastSyncedHash: make(map[string]string),

		staticDB:         db,
		staticLogger:     logger,
		staticPortalURLs: portalURLs,
		staticStopChan:   make(chan struct{}),
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
	s.staticWaitGroup.Add(1)
	go func() {
		s.threadedSyncLoop()
		s.staticWaitGroup.Done()
	}()

	return nil
}

// Stop waits for the syncer's waitgroup and times out after one minute.
func (s *Syncer) Stop() error {
	// check whether the syncer was started
	s.staticMu.Lock()
	if !s.started {
		s.staticMu.Unlock()
		return errors.New("syncer not started")
	}
	s.started = false
	s.staticMu.Unlock()

	// stop the syncer by closing the stop channel
	close(s.staticStopChan)

	// wait for the waitgroup, timeout and signal unclean shutdown after 1m
	c := make(chan struct{})
	go func() {
		defer close(c)
		s.staticWaitGroup.Wait()
	}()
	select {
	case <-c:
		return nil
	case <-time.After(stopTimeoutDuration):
		return errors.New("unclean syncer shutdown")
	}
}

// threadedSyncLoop holds the main sync loop
func (s *Syncer) threadedSyncLoop() {
	// convenience variables
	logger := s.staticLogger

	for {
		err := s.managedSyncPortals()
		if err != nil {
			logger.Errorf("failed to sync portals with skyd, error %v", err)
		}

		select {
		case <-s.staticStopChan:
			return
		case <-time.After(syncInterval):
		}
	}
}

// managedLastSyncedHash returns the last synced hash, as a string, for the
// given portal URL
func (s *Syncer) managedLastSyncedHash(portalURL string) string {
	s.staticMu.Lock()
	s.staticMu.Unlock()
	return s.lastSyncedHash[portalURL]
}

// managedSyncPortals will sync the blocklist of all portals defined on the
// syncer with the local skyd.
func (s *Syncer) managedSyncPortals() error {
	// convenience variables
	logger := s.staticLogger

	// sync all portals one by one
	var errs []error
	for _, portalURL := range s.staticPortalURLs {
		logger.Infof("syncing blocklist for portal '%s'", portalURL)

		// create a client and fetch the last synced hash
		client := api.NewSkydClient(portalURL, "")
		lastSynced := s.managedLastSyncedHash(portalURL)
		reporter := database.Reporter{Name: portalURL}

		// define loop variables
		offset := 0
		hasMore := true
		seen := false

		// fetch all entries
		var hashes []database.BlockedSkylink
		for hasMore && !seen {
			// fetch at current offset
			blg, err := client.BlocklistGET(offset)
			if err != nil {
				errs = append(errs, errors.AddContext(err, fmt.Sprintf("could not get blocklist for portal %s", portalURL)))
				break
			}

			// update loop state
			hasMore = blg.HasMore
			offset += len(blg.Entries)

			// check whether we're seeing entries we know already
			for _, entry := range blg.Entries {
				hash := database.Hash{entry.Hash}
				if lastSynced != "" && hash.String() == lastSynced {
					seen = true
					break
				}

				hashes = append(hashes, database.BlockedSkylink{
					Hash:           hash,
					Reporter:       reporter,
					Tags:           entry.Tags,
					TimestampAdded: time.Now().UTC(),
				})
			}
		}

		// continue if no hashes were found
		if len(hashes) == 0 {
			logger.Infof("could not find any hashes for portal '%s'", portalURL)
			continue
		}

		// create context
		ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)

		// bulk insert all of the hashes into the database
		added, err := s.staticDB.CreateBlockedSkylinkBulk(ctx, hashes)
		if err != nil {
			cancel()
			logger.Errorf("failed inserting hashes from '%s' into our database, err '%v'", portalURL, err)
			continue
		}

		cancel()
		logger.Infof("added %v hashes from portal '%s'", added, portalURL)

		// update the last synced hash to avoid paging through the entire
		// blocklist in consecutive syncs
		last := hashes[len(hashes)-1]
		s.managedUpdateLastSyncedHash(portalURL, last.Hash.String())
	}

	return errors.Compose(errs...)
}

// managedUpdateLastSyncedHash updates the last synced hash for the given portal
func (s *Syncer) managedUpdateLastSyncedHash(portalURL string, hash string) {
	s.staticMu.Lock()
	defer s.staticMu.Unlock()
	s.lastSyncedHash[portalURL] = hash
}
