package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

const (
	// mongoDefaultTimeout is the timeout for the context used in testing
	// whenever a context is sent to mongo
	mongoDefaultTimeout = time.Minute

	// mongoIndexCreateTimeout is the timeout used when creating indices
	mongoIndexCreateTimeout = 10 * time.Second
)

var (
	// ErrIndexCreateFailed is returned when an error occurred when trying to
	// ensure an index
	ErrIndexCreateFailed = errors.New("failed to create index")

	// ErrNoDocumentsFound is returned when a database operation completes
	// successfully but it doesn't find or affect any documents.
	ErrNoDocumentsFound = errors.New("no documents")

	// ErrNoEntriesUpdated is returned when no entries were updated after an
	// update was performed.
	ErrNoEntriesUpdated = errors.New("no entries updated")

	// ErrSkylinkExists is returned when we try to add a skylink to the database
	// and it already exists there.
	ErrSkylinkExists = errors.New("skylink already exists")

	// ServerDomain is the unique server name, e.g. eu-pol-4.siasky.net
	ServerDomain string

	// True is a helper value, so we can pass a *bool to MongoDB's methods.
	True = true

	// dbName defines the name of the database this service uses
	dbName = "blocker"
	// dbSkylinks defines the name of the skylinks collection
	dbSkylinks = "skylinks"
	// dbAllowList defines the name of the allowlist collection
	dbAllowList = "allowlist"
	// dbLatestBlockTimestamps dbLatestBlockTimestamps
	dbLatestBlockTimestamps = "latest_block_timestamps"
)

// DB holds a connection to the database, as well as helpful shortcuts to
// collections and utilities.
type DB struct {
	ctx             context.Context
	staticClient    *mongo.Client
	staticDB        *mongo.Database
	staticAllowList *mongo.Collection
	staticSkylinks  *mongo.Collection
	staticLogger    *logrus.Logger
}

// New creates a new database connection.
func New(ctx context.Context, uri string, creds options.Credential, logger *logrus.Logger) (*DB, error) {
	return NewCustomDB(ctx, uri, dbName, creds, logger)
}

// NewCustomDB creates a new database connection to a database with a custom
// name.
func NewCustomDB(ctx context.Context, uri string, dbName string, creds options.Credential, logger *logrus.Logger) (*DB, error) {
	if ctx == nil {
		return nil, errors.New("invalid context provided")
	}
	if logger == nil {
		return nil, errors.New("invalid logger provided")
	}

	// Define a new context with a timeout to handle the database setup.
	dbCtx, cancel := context.WithTimeout(ctx, mongoDefaultTimeout)
	defer cancel()

	// Prepare the options for connecting to the db.
	opts := options.Client().
		ApplyURI(uri).
		SetAuth(creds).
		SetReadPreference(readpref.Primary()).
		SetWriteConcern(writeconcern.New(
			writeconcern.WMajority(),
			writeconcern.WTimeout(time.Second*30),
		)).
		SetCompressors([]string{"zstd,zlib,snappy"})

	c, err := mongo.NewClient(opts)
	if err != nil {
		return nil, errors.AddContext(err, "failed to create a new db client")
	}
	err = c.Connect(dbCtx)
	if err != nil {
		return nil, errors.AddContext(err, "failed to connect to db")
	}
	db := c.Database(dbName)
	err = ensureDBSchema(dbCtx, db, logger)
	if err != nil && errors.Contains(err, ErrIndexCreateFailed) {
		// We do not error out if we failed to ensure the existence of an index.
		// It is definitely an issue that should be looked into, which is why we
		// tag it as [CRITICAL], but seeing as the blocker will work the same
		// without the index it's no reason to prevent it from running.
		logger.Errorf(`[CRITICAL] failed to ensure DB schema, err: %v`, err)
	} else if err != nil {
		return nil, err
	}
	return &DB{
		ctx:             ctx,
		staticClient:    c,
		staticDB:        db,
		staticAllowList: db.Collection(dbAllowList),
		staticSkylinks:  db.Collection(dbSkylinks),
		staticLogger:    logger,
	}, nil
}

// Close disconnects the db.
func (db *DB) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return db.staticClient.Disconnect(ctx)
}

// Ping sends a ping command to verify that the client can connect to the DB and
// specifically to the primary.
func (db *DB) Ping(ctx context.Context) error {
	return db.staticDB.Client().Ping(ctx, readpref.Primary())
}

// BlockedSkylink fetches the DB record that corresponds to the given skylink
// from the database.
func (db *DB) BlockedSkylink(ctx context.Context, s string) (*BlockedSkylink, error) {
	sr := db.staticSkylinks.FindOne(ctx, bson.M{"skylink": s})
	if sr.Err() != nil {
		return nil, sr.Err()
	}
	var sl BlockedSkylink
	err := sr.Decode(&sl)
	if err != nil {
		return nil, err
	}
	return &sl, nil
}

// CreateBlockedSkylink creates a new skylink. If the skylink already exists it
// does nothing.
func (db *DB) CreateBlockedSkylink(ctx context.Context, skylink *BlockedSkylink) error {
	_, err := db.staticSkylinks.InsertOne(ctx, skylink)
	if err != nil && strings.Contains(err.Error(), "E11000 duplicate key error collection") {
		db.staticLogger.Debugf("CreateBlockedSkylink: duplicate key, returning '%s'", ErrSkylinkExists.Error())
		// This skylink already exists in the DB.
		return ErrSkylinkExists
	}
	if err != nil {
		db.staticLogger.Debugf("CreateBlockedSkylink: mongodb error '%s'", err.Error())
	}
	return err
}

// IsAllowListed returns whether the given skylink is on the allow list.
func (db *DB) IsAllowListed(ctx context.Context, skylink string) (bool, error) {
	res := db.staticAllowList.FindOne(ctx, bson.M{"skylink": skylink})
	if isDocumentNotFound(res.Err()) {
		return false, nil
	}
	if res.Err() != nil {
		return false, res.Err()
	}
	return true, nil
}

// MarkAsSucceeded will toggle the failed flag for all documents in the given
// list of skylinks that are currently marked as failed.
func (db *DB) MarkAsSucceeded(skylinks []BlockedSkylink) error {
	return db.updateFailedFlag(skylinks, false)
}

// MarkAsFailed will mark the given documents as failed
func (db *DB) MarkAsFailed(skylinks []BlockedSkylink) error {
	return db.updateFailedFlag(skylinks, true)
}

// BlockedSkylinkSave saves the given BlockedSkylink record to the database.
// NOTE: commented out since this method isn't used or tested.
//func (db *DB) BlockedSkylinkSave(ctx context.Context, skylink *BlockedSkylink) error {
//	filter := bson.M{"_id": skylink.ID}
//	opts := &options.ReplaceOptions{
//		Upsert: &True,
//	}
//	_, err := db.staticSkylinks.ReplaceOne(ctx, filter, skylink, opts)
//	if err != nil {
//		return errors.AddContext(err, "failed to save")
//	}
//	return nil
//}

// SkylinksToBlock sweeps the database for new skylinks. It uses the latest
// block timestamp for this server which is retrieves from the DB. It scans all
// blocked skylinks from the hour before that timestamp, too, in order to
// protect against system clock float.
func (db *DB) SkylinksToBlock() ([]BlockedSkylink, error) {
	cutoff, err := db.LatestBlockTimestamp()
	if err != nil {
		return nil, errors.AddContext(err, "failed to fetch the latest timestamp from the DB")
	}
	// Push cutoff one hour into the past in order to compensate of any
	// potential system time drift.
	cutoff = cutoff.Add(-time.Hour)
	db.staticLogger.Tracef("SkylinksToBlock: fetching all skylinks added after cutoff of %s", cutoff.String())

	filter := bson.M{
		"timestamp_added": bson.M{"$gt": cutoff},
		"failed":          bson.M{"$ne": true},
	}
	opts := options.Find()
	opts.SetSort(bson.D{{"timestamp_added", 1}})
	c, err := db.staticDB.Collection(dbSkylinks).Find(db.ctx, filter, opts)
	if err != nil && errors.Contains(err, ErrNoDocumentsFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.AddContext(err, "failed to fetch skylinks from the DB")
	}

	list := make([]BlockedSkylink, 0)
	err = c.All(db.ctx, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// SkylinksToRetry returns all skylinks that failed to get blocked the first
// time around. This is a retry mechanism to ensure we keep retrying to block
// those skylinks, but at the same try 'unblock' the main block loop in order
// for it to run smoothly.
func (db *DB) SkylinksToRetry() ([]BlockedSkylink, error) {
	filter := bson.M{"failed": bson.M{"$eq": true}}
	opts := options.Find()
	opts.SetSort(bson.D{{"timestamp_added", 1}})
	c, err := db.staticDB.Collection(dbSkylinks).Find(db.ctx, filter, opts)
	if err != nil && errors.Contains(err, ErrNoDocumentsFound) {
		return nil, nil
	} else if err != nil {
		return nil, errors.AddContext(err, "failed to fetch skylinks from the DB")
	}

	list := make([]BlockedSkylink, 0)
	err = c.All(db.ctx, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// LatestBlockTimestamp returns the timestamp (timestampAdded) of the latest
// skylink that was blocked. When fetching new SkylinksToBlock we should start
// from that timestamp (and one hour before that).
func (db *DB) LatestBlockTimestamp() (time.Time, error) {
	sr := db.staticDB.Collection(dbLatestBlockTimestamps).FindOne(db.ctx, bson.M{"server_name": ServerDomain})
	if sr.Err() != nil && sr.Err() != mongo.ErrNoDocuments {
		return time.Time{}, sr.Err()
	}
	if sr.Err() == mongo.ErrNoDocuments {
		return time.Time{}, nil
	}
	var payload struct {
		LatestBlock time.Time `bson:"latest_block"`
	}
	err := sr.Decode(&payload)
	if err != nil {
		return time.Time{}, errors.AddContext(err, "failed to deserialize the value from the DB")
	}
	return payload.LatestBlock, nil
}

// SetLatestBlockTimestamp sets the timestamp (timestampAdded) of the latest
// skylink that was blocked. When fetching new SkylinksToBlock we should start
// from that timestamp (and one hour before that).
func (db *DB) SetLatestBlockTimestamp(t time.Time) error {
	filter := bson.M{"server_name": ServerDomain}
	value := bson.M{"$set": bson.M{"server_name": ServerDomain, "latest_block": t}}
	opts := options.UpdateOptions{Upsert: &True}
	ur, err := db.staticDB.Collection(dbLatestBlockTimestamps).UpdateOne(db.ctx, filter, value, &opts)
	if err != nil {
		return errors.AddContext(err, "failed to update")
	}
	if ur.ModifiedCount+ur.UpsertedCount == 0 {
		return ErrNoEntriesUpdated
	}
	return nil
}

// updateFailedFlag is a helper method that updates the failed flag on the
// documents that correspond with the skylinks in the given array.
func (db *DB) updateFailedFlag(skylinks []BlockedSkylink, failed bool) error {
	// extract all ids
	ids := make([]primitive.ObjectID, len(skylinks))
	for i, sl := range skylinks {
		ids[i] = sl.ID
	}

	// create the filter, make sure to specify currently unblocked skylinks
	filter := bson.M{
		"_id":    bson.M{"$in": ids},
		"failed": bson.M{"$eq": !failed},
	}

	// define the update
	update := bson.M{
		"$set": bson.M{
			"failed": failed,
		},
	}

	// perform the update
	collSkylinks := db.staticDB.Collection(dbSkylinks)
	_, err := collSkylinks.UpdateMany(db.ctx, filter, update)
	return err
}

// ensureDBSchema checks that we have all collections and indexes we need and
// creates them if needed.
// See https://docs.mongodb.com/manual/indexes/
// See https://docs.mongodb.com/manual/core/index-unique/
func ensureDBSchema(ctx context.Context, db *mongo.Database, log *logrus.Logger) error {
	// schema defines a mapping between a collection name and the indexes that
	// must exist for that collection.
	schema := map[string][]mongo.IndexModel{
		dbAllowList: {
			{
				Keys:    bson.D{{"skylink", 1}},
				Options: options.Index().SetName("skylink").SetUnique(true),
			},
			{
				Keys:    bson.D{{"timestamp_added", 1}},
				Options: options.Index().SetName("timestamp_added"),
			},
		},
		dbSkylinks: {
			{
				Keys:    bson.D{{"skylink", 1}},
				Options: options.Index().SetName("skylink").SetUnique(true),
			},
			{
				Keys:    bson.D{{"timestamp_added", 1}},
				Options: options.Index().SetName("timestamp_added"),
			},
			{
				Keys:    bson.D{{"failed", 1}},
				Options: options.Index().SetName("failed"),
			},
		},
		dbLatestBlockTimestamps: {
			{
				Keys:    bson.D{{"server_name", 1}},
				Options: options.Index().SetName("server_name").SetUnique(true),
			},
		},
	}

	icOpts := options.CreateIndexes().SetMaxTime(mongoIndexCreateTimeout)

	var icErr error
	for collName, models := range schema {
		coll, err := ensureCollection(ctx, db, collName)
		if err != nil {
			// no need to continue if ensuring a collection fails
			return err
		}

		iv := coll.Indexes()
		names, err := iv.CreateMany(ctx, models, icOpts)
		if err != nil {
			// if the index creation fails, compose the error but continue to
			// try and ensure the rest of the database schema
			icErr = errors.Compose(icErr, errors.AddContext(err, fmt.Sprintf("collection '%v'", collName)))
			continue
		}
		log.Debugf("Ensured index exists: %v | %v", collName, names)
	}
	if icErr != nil {
		return errors.Compose(icErr, ErrIndexCreateFailed)
	}
	return nil
}

// ensureCollection gets the given collection from the
// database and creates it if it doesn't exist.
func ensureCollection(ctx context.Context, db *mongo.Database, collName string) (*mongo.Collection, error) {
	coll := db.Collection(collName)
	if coll == nil {
		err := db.CreateCollection(ctx, collName)
		if err != nil {
			return nil, err
		}
		coll = db.Collection(collName)
		if coll == nil {
			return nil, errors.New("failed to create collection " + collName)
		}
	}
	return coll, nil
}

// isDocumentNotFound is a helper function that returns whether the given error
// contains the mongo documents not found error message.
func isDocumentNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrNoDocumentsFound.Error())
}
