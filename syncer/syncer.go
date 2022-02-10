package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"go.sia.tech/siad/build"
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
		staticCtx     context.Context
		staticDB      *database.DB
		staticLogger  *logrus.Logger
		staticSkydAPI skyd.API
	}
)

// New returns a new Syncer with the given parameters.
func New(ctx context.Context, skydAPI skyd.API, db *database.DB, logger *logrus.Logger) (*Syncer, error) {
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
		staticCtx:     ctx,
		staticDB:      db,
		staticLogger:  logger,
		staticSkydAPI: skydAPI,
	}
	return s, nil
}

// Start launches a background task that periodically syncs the blocklists of
// the preconfigured portals with the blocklist of the local skyd instance.
func (s *Syncer) Start() {
	// Start the syncing loop.
	go func() {
		for {
			select {
			case <-s.staticCtx.Done():
				return
			case <-time.After(syncInterval):
			}
			err := s.sync()
			if err != nil {
				s.staticLogger.Debugf("Sync error: %s", err.Error())
				continue
			}
			s.staticLogger.Debugf("Sync ran successfully.")
		}
	}()
}

// sync will loop through the preconfigured portals and pull their blocklist and
// ensure all of the hashes on those blocklists are blocked in the local skyd
// instance.
func (s *Syncer) sync() error {
	return nil
}
