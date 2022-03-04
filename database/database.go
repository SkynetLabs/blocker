package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"go.sia.tech/siad/crypto"
)

const (
	// MongoDefaultTimeout is the timeout for the context used in testing
	// whenever a context is sent to mongo
	MongoDefaultTimeout = 10 * time.Minute

	// mongoIndexCreateTimeout is the timeout used when creating indices
	mongoIndexCreateTimeout = 10 * time.Minute
)

var (
	// ErrDuplicateKey is returned when an insert is attempted that violates the
	// unique constraint on a certain field.
	ErrDuplicateKey = errors.New("E11000 duplicate key")

	// ErrIndexCreateFailed is returned when an error occurred when trying to
	// ensure an index
	ErrIndexCreateFailed = errors.New("failed to create index")

	// ErrIndexDropFailed is returned when an error occurred when trying to
	// drop an index
	ErrIndexDropFailed = errors.New("failed to drop an index")

	// ErrNoDocumentsFound is returned when a database operation completes
	// successfully but it doesn't find or affect any documents.
	ErrNoDocumentsFound = errors.New("no documents")

	// ErrNoEntriesUpdated is returned when no entries were updated after an
	// update was performed.
	ErrNoEntriesUpdated = errors.New("no entries updated")

	// ErrSkylinkExists is returned when we try to add a skylink to the database
	// and it already exists there.
	ErrSkylinkExists = errors.New("skylink already exists")

	// ServerUID is a random string that uniquely identifies the server
	ServerUID string

	// True is a helper value, so we can pass a *bool to MongoDB's methods.
	True = true

	// dbName defines the name of the database this service uses
	dbName = "blocker"

	// collSkylinks defines the name of the skylinks collection
	collSkylinks = "skylinks"

	// collAllowlist defines the name of the allowlist collection
	collAllowlist = "allowlist"

	// collLatestBlockTimestamps collLatestBlockTimestamps
	collLatestBlockTimestamps = "latest_block_timestamps"
)

// DB holds a connection to the database, as well as helpful shortcuts to
// collections and utilities.
//
// NOTE: update the 'Purge' method when adding new collections
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
	dbCtx, cancel := context.WithTimeout(ctx, MongoDefaultTimeout)
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

	// Ensure the database schema
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

	// Define the database
	cdb := &DB{
		ctx:             ctx,
		staticClient:    c,
		staticDB:        db,
		staticAllowList: db.Collection(collAllowlist),
		staticSkylinks:  db.Collection(collSkylinks),
		staticLogger:    logger,
	}

	return cdb, nil
}

// BlockedHashes allows to pass a skip and limit parameter and returns an array
// of blocked hashes alongside a boolean that indicates whether there's more
// documents after the current 'page'.
func (db *DB) BlockedHashes(sort, skip, limit int) ([]BlockedSkylink, bool, error) {
	// configure the options
	opts := options.Find()
	opts.SetSkip(int64(skip))
	opts.SetLimit(int64(limit + 1))
	opts.SetSort(bson.D{{"timestamp_added", sort}})

	// fetch the documents
	docs, err := db.find(db.ctx, bson.M{"invalid": bson.M{"$ne": true}}, opts)
	if err != nil {
		return nil, false, err
	}

	// we have done the find with "limit+1" because that allows us to return
	// whether there are "more" documents after the given offset, we however do
	// not want to return this document, but instead return 'true' if it existed
	if len(docs) > int(limit) {
		return docs[:limit], true, nil
	}
	return docs, false, nil
}

// Close disconnects the db.
func (db *DB) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return db.staticClient.Disconnect(ctx)
}

// CreateBlockedSkylink creates a new skylink. If the skylink already exists it
// does nothing.
func (db *DB) CreateBlockedSkylink(ctx context.Context, skylink *BlockedSkylink) error {
	// Ensure the hash is set
	if skylink.Hash == (Hash{}) {
		return errors.New("unexpected blocked skylink, 'hash' is not set")
	}

	// Insert the skylink
	_, err := db.staticSkylinks.InsertOne(ctx, skylink)
	if isDuplicateKey(err) {
		return ErrSkylinkExists
	}
	if err != nil {
		db.staticLogger.Debugf("CreateBlockedSkylink: mongodb error '%v'", err)
		return err
	}
	return nil
}

// CreateAllowListedSkylink creates a new allowlisted skylink. If the skylink
// already exists it does nothing and returns without failure.
func (db *DB) CreateAllowListedSkylink(ctx context.Context, skylink *AllowListedSkylink) error {
	// Insert the skylink
	_, err := db.staticAllowList.InsertOne(ctx, skylink)
	if err != nil && !isDuplicateKey(err) {
		return err
	}
	return nil
}

// FindByHash fetches the DB record that corresponds to the given hash
// from the database.
func (db *DB) FindByHash(ctx context.Context, hash Hash) (*BlockedSkylink, error) {
	return db.findOne(ctx, bson.M{"hash": hash.String()})
}

// IsAllowListed returns whether the given skylink is on the allow list.
func (db *DB) IsAllowListed(ctx context.Context, hash crypto.Hash) (bool, error) {
	res := db.staticAllowList.FindOne(ctx, bson.M{"hash": hash.String()})
	if isDocumentNotFound(res.Err()) {
		return false, nil
	}
	if res.Err() != nil {
		return false, res.Err()
	}
	return true, nil
}

// MarkAsSucceeded will toggle the failed flag for all documents in the given
// list of hashes that are currently marked as failed.
func (db *DB) MarkAsSucceeded(hashes []Hash) error {
	return db.updateFailedFlag(hashes, false)
}

// MarkAsFailed will mark the given documents as failed
func (db *DB) MarkAsFailed(hashes []Hash) error {
	return db.updateFailedFlag(hashes, true)
}

// Ping sends a ping command to verify that the client can connect to the DB and
// specifically to the primary.
func (db *DB) Ping(ctx context.Context) error {
	return db.staticDB.Client().Ping(ctx, readpref.Primary())
}

// Purge deletes all documents from all collections in the database
//
// NOTE: this function should never be called in production and should only be
// used for testing purposes
func (db *DB) Purge(ctx context.Context) error {
	_, err := db.staticSkylinks.DeleteMany(ctx, bson.D{})
	if err != nil {
		return errors.AddContext(err, "failed to purge skylinks collection")
	}
	_, err = db.staticAllowList.DeleteMany(ctx, bson.D{})
	if err != nil {
		return errors.AddContext(err, "failed to purge allowlist collection")
	}
	return nil
}

// HashesToBlock sweeps the database for unblocked hashes. It uses the latest
// block timestamp for this server which is retrieves from the DB. It scans all
// blocked skylinks from the hour before that timestamp, too, in order to
// protect against system clock float.
func (db *DB) HashesToBlock() ([]Hash, error) {
	cutoff, err := db.LatestBlockTimestamp()
	if err != nil {
		return nil, errors.AddContext(err, "failed to fetch the latest timestamp from the DB")
	}
	// Push cutoff one hour into the past in order to compensate of any
	// potential system time drift.
	cutoff = cutoff.Add(-time.Hour)
	db.staticLogger.Tracef("HashesToBlock: fetching all hashes added after cutoff of %s", cutoff.String())

	filter := bson.M{
		"timestamp_added": bson.M{"$gt": cutoff},
		"failed":          bson.M{"$ne": true},
	}
	opts := options.Find()
	opts.SetSort(bson.D{{"timestamp_added", 1}})
	opts.SetProjection(bson.D{{"hash", 1}})

	docs, err := db.find(db.ctx, filter, opts)
	if err != nil {
		return nil, err
	}

	// Extract the hashes
	hashes := make([]Hash, len(docs))
	for i, doc := range docs {
		hashes[i] = doc.Hash
	}
	return hashes, nil
}

// HashesToRetry returns all hashes that failed to get blocked the first time
// around. This is a retry mechanism to ensure we keep retrying to block those
// hashes, but at the same try 'unblock' the main block loop in order for it
// to run smoothly.
func (db *DB) HashesToRetry() ([]Hash, error) {
	filter := bson.M{"failed": bson.M{"$eq": true}}
	opts := options.Find()
	opts.SetSort(bson.D{{"timestamp_added", 1}})
	opts.SetProjection(bson.D{{"hash", 1}})

	docs, err := db.find(db.ctx, filter, opts)
	if err != nil {
		return nil, err
	}

	// Extract the hashes
	hashes := make([]Hash, len(docs))
	for i, doc := range docs {
		hashes[i] = doc.Hash
	}
	return hashes, nil
}

// LatestBlockTimestamp returns the timestamp (timestampAdded) of the latest
// skylink that was blocked. When fetching new SkylinksToBlock we should start
// from that timestamp (and one hour before that).
func (db *DB) LatestBlockTimestamp() (time.Time, error) {
	sr := db.staticDB.Collection(collLatestBlockTimestamps).FindOne(db.ctx, bson.M{"server_name": ServerUID})
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
	filter := bson.M{"server_name": ServerUID}
	value := bson.M{"$set": bson.M{"server_name": ServerUID, "latest_block": t}}
	opts := options.UpdateOptions{Upsert: &True}
	ur, err := db.staticDB.Collection(collLatestBlockTimestamps).UpdateOne(db.ctx, filter, value, &opts)
	if err != nil {
		return errors.AddContext(err, "failed to update")
	}
	if ur.ModifiedCount+ur.UpsertedCount == 0 {
		return ErrNoEntriesUpdated
	}
	return nil
}

// find wraps the `Find` function on the Skylinks collection and returns an
// array of decoded blocked skylink objects
func (db *DB) find(ctx context.Context, filter interface{},
	opts ...*options.FindOptions) ([]BlockedSkylink, error) {
	c, err := db.staticDB.Collection(collSkylinks).Find(ctx, filter, opts...)
	if isDocumentNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	list := make([]BlockedSkylink, 0)
	err = c.All(db.ctx, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

// findOne wraps the `FindOne` function on the Skylinks collection and returns
// a decoded blocked skylink object
func (db *DB) findOne(ctx context.Context, filter interface{},
	opts ...*options.FindOneOptions) (*BlockedSkylink, error) {
	sr := db.staticDB.Collection(collSkylinks).FindOne(ctx, filter, opts...)
	if isDocumentNotFound(sr.Err()) {
		return nil, nil
	}
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

// updateFailedFlag is a helper method that updates the failed flag on the
// documents that correspond with the skylinks in the given array.
func (db *DB) updateFailedFlag(hashes []Hash, failed bool) error {
	// return early if no hashes were given
	if len(hashes) == 0 {
		return nil
	}

	// create the filter, make sure to specify currently unblocked skylinks
	filter := bson.M{
		"hash":   bson.M{"$in": hashes},
		"failed": bson.M{"$eq": !failed},
	}

	// define the update
	update := bson.M{
		"$set": bson.M{
			"failed": failed,
		},
	}

	// perform the update
	collSkylinks := db.staticDB.Collection(collSkylinks)
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
		collAllowlist: {
			{
				Keys:    bson.D{{"hash", 1}},
				Options: options.Index().SetName("hash").SetUnique(true),
			},
			{
				Keys:    bson.D{{"timestamp_added", 1}},
				Options: options.Index().SetName("timestamp_added"),
			},
		},
		collSkylinks: {
			{
				Keys:    bson.D{{"hash", 1}},
				Options: options.Index().SetName("hash").SetUnique(true),
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
		collLatestBlockTimestamps: {
			{
				Keys:    bson.D{{"server_name", 1}},
				Options: options.Index().SetName("server_name").SetUnique(true),
			},
		},
	}

	// build the options
	opts := options.CreateIndexes()
	opts.SetMaxTime(mongoIndexCreateTimeout)
	opts.SetCommitQuorumString("majority") // defaults to all

	// ensure all collections and indices exist
	var createErr error
	for collName, models := range schema {
		coll, err := ensureCollection(ctx, db, collName)
		if err != nil {
			// no need to continue if ensuring a collection fails
			return err
		}

		iv := coll.Indexes()
		names, err := iv.CreateMany(ctx, models, opts)
		if err != nil {
			// if the index creation fails, compose the error but continue to
			// try and ensure the rest of the database schema
			createErr = errors.Compose(createErr, errors.AddContext(err, fmt.Sprintf("collection '%v'", collName)))
			continue
		}

		log.Debugf("Ensured index exists: %v | %v", collName, names)
	}
	if createErr != nil {
		createErr = errors.Compose(createErr, ErrIndexCreateFailed)
	}

	// drop the old indices on 'skylink'
	_, err1 := dropIndex(ctx, db.Collection(collAllowlist), "skylink")
	_, err2 := dropIndex(ctx, db.Collection(collSkylinks), "skylink")
	dropErr := errors.Compose(err1, err2)
	if dropErr != nil {
		dropErr = errors.Compose(dropErr, ErrIndexDropFailed)
	}

	return errors.Compose(createErr, dropErr)
}

// dropIndex is a helper function that drops the index with given name on the
// given collection
func dropIndex(ctx context.Context, coll *mongo.Collection, indexName string) (bool, error) {
	hasIndex, err := hasIndex(ctx, coll, indexName)
	if err != nil {
		return false, err
	}

	if !hasIndex {
		return false, nil
	}

	_, err = coll.Indexes().DropOne(ctx, indexName)
	if err != nil {
		return false, err
	}

	return true, nil
}

// hasIndex is a helper function that returns true if the given collection has
// an index with given name
func hasIndex(ctx context.Context, coll *mongo.Collection, indexName string) (bool, error) {
	idxs := coll.Indexes()

	cur, err := idxs.List(ctx)
	if err != nil {
		return false, err
	}

	var result []bson.M
	err = cur.All(ctx, &result)
	if err != nil {
		return false, err
	}

	found := false
	for _, v := range result {
		for k, v1 := range v {
			if k == "name" && v1 == indexName {
				found = true
			}
		}
	}
	return found, nil
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

// isDuplicateKey is a helper function that returns whether the given error
// contains the mongo duplicate key error message.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), ErrDuplicateKey.Error())
}
