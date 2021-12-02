package database

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/SkynetLabs/skynet-accounts/database"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	// ErrNoDocumentsFound is returned when a database operation completes
	// successfully but it doesn't find or affect any documents.
	ErrNoDocumentsFound = errors.New("no documents found")
	// ErrSkylinkExists is returned when we try to add a skylink to the database
	// and it already exists there.
	ErrSkylinkExists = errors.New("skylink already exists")

	// Portal is the preferred portal to use, e.g. https://siasky.net
	Portal string
	// ServerDomain is the unique server name, e.g. eu-pol-4.siasky.net
	ServerDomain string

	// True is a helper value, so we can pass a *bool to MongoDB's methods.
	True = true

	// dbName defines the name of the database this service uses
	dbName = "blocker"
	// dbSkylinks defines the name of the skylinks collection
	dbSkylinks = "skylinks"
	// dbLatestBlockTimestamps dbLatestBlockTimestamps
	dbLatestBlockTimestamps = "latest_block_timestamps"
)

// DB holds a connection to the database, as well as helpful shortcuts to
// collections and utilities.
type DB struct {
	Ctx      context.Context
	DB       *mongo.Database
	Skylinks *mongo.Collection
	Logger   *logrus.Logger
}

// New creates a new database connection.
func New(ctx context.Context, creds database.DBCredentials, logger *logrus.Logger) (*DB, error) {
	return NewCustomDB(ctx, dbName, creds, logger)
}

// NewCustomDB creates a new database connection to a database with a custom name.
func NewCustomDB(ctx context.Context, dbName string, creds database.DBCredentials, logger *logrus.Logger) (*DB, error) {
	if ctx == nil {
		return nil, errors.New("invalid context provided")
	}
	if logger == nil {
		return nil, errors.New("invalid logger provided")
	}
	c, err := mongo.NewClient(options.Client().ApplyURI(connectionString(creds)))
	if err != nil {
		return nil, errors.AddContext(err, "failed to create a new db client")
	}
	err = c.Connect(ctx)
	if err != nil {
		return nil, errors.AddContext(err, "failed to connect to db")
	}
	db := c.Database(dbName)
	err = ensureDBSchema(ctx, db, logger)
	if err != nil {
		return nil, err
	}
	return &DB{
		Ctx:      ctx,
		DB:       db,
		Skylinks: db.Collection(dbSkylinks),
		Logger:   logger,
	}, nil
}

// Ping sends a ping command to verify that the client can connect to the DB and
// specifically to the primary.
func (db *DB) Ping(ctx context.Context) error {
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return db.DB.Client().Ping(ctx2, readpref.Primary())
}

// BlockedSkylink fetches the DB record that corresponds to the given skylink
// from the database.
func (db *DB) BlockedSkylink(ctx context.Context, s string) (*BlockedSkylink, error) {
	sr := db.Skylinks.FindOne(ctx, bson.M{"skylink": s})
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

// BlockedSkylinkByID fetches the DB record that corresponds to the given skylink by
// its DB ID.
func (db *DB) BlockedSkylinkByID(ctx context.Context, id primitive.ObjectID) (*BlockedSkylink, error) {
	sr := db.Skylinks.FindOne(ctx, bson.M{"_id": id})
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

// BlockedSkylinkCreate creates a new skylink. If the skylink already exists it does
// nothing.
func (db *DB) BlockedSkylinkCreate(ctx context.Context, skylink *BlockedSkylink) error {
	_, err := db.Skylinks.InsertOne(ctx, skylink)
	if err != nil && strings.Contains(err.Error(), "E11000 duplicate key error collection") {
		db.Logger.Debugf("BlockedSkylinkCreate: duplicate key, returning '%s'", ErrSkylinkExists.Error())
		// This skylink already exists in the DB.
		return ErrSkylinkExists
	}
	if err != nil {
		db.Logger.Debugf("BlockedSkylinkCreate: mongodb error '%s'", err.Error())
	}
	return err
}

// BlockedSkylinkSave saves the given BlockedSkylink record to the database.
func (db *DB) BlockedSkylinkSave(ctx context.Context, skylink *BlockedSkylink) error {
	filter := bson.M{"_id": skylink.ID}
	opts := &options.ReplaceOptions{
		Upsert: &True,
	}
	_, err := db.Skylinks.ReplaceOne(ctx, filter, skylink, opts)
	if err != nil {
		return errors.AddContext(err, "failed to save")
	}
	return nil
}

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
	db.Logger.Tracef("SkylinksToBlock: fetching all skylinks added after cutoff of %s", cutoff.String())

	filter := bson.M{"timestamp_added": bson.M{"$gt": cutoff}}
	c, err := db.DB.Collection(dbSkylinks).Find(db.Ctx, filter)
	if err != nil {
		return nil, errors.AddContext(err, "failed to fetch skylinks from the DB")
	}
	list := make([]BlockedSkylink, 0)
	err = c.All(db.Ctx, &list)
	if err != nil {
		return nil, err
	}
	db.Logger.Tracef("SkylinksToBlock: returning list %v", list)
	return list, nil
}

// LatestBlockTimestamp returns the timestamp (timestampAdded) of the latest
// skylink that was blocked. When fetching new SkylinksToBlock we should start
// from that timestamp (and one hour before that).
func (db *DB) LatestBlockTimestamp() (time.Time, error) {
	sr := db.DB.Collection(dbLatestBlockTimestamps).FindOne(db.Ctx, bson.M{"server_name": ServerDomain})
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
	ur, err := db.DB.Collection(dbLatestBlockTimestamps).UpdateOne(db.Ctx, filter, value, &opts)
	if err != nil {
		return errors.AddContext(err, "failed to update")
	}
	if ur.ModifiedCount+ur.UpsertedCount == 0 {
		return errors.New("no entries updated")
	}
	return nil
}

// connectionString is a helper that returns a valid MongoDB connection string
// based on the passed credentials and a set of constants. The connection string
// is using the standalone approach because the service is supposed to talk to
// the replica set only via the local node.
// See https://docs.mongodb.com/manual/reference/connection-string/
func connectionString(creds database.DBCredentials) string {
	// There are some symbols in usernames and passwords that need to be escaped.
	// See https://docs.mongodb.com/manual/reference/connection-string/#components
	return fmt.Sprintf(
		"mongodb://%s:%s@%s:%s/?compressors=%s&readPreference=%s&w=%s&wtimeoutMS=%s",
		url.QueryEscape(creds.User),
		url.QueryEscape(creds.Password),
		creds.Host,
		creds.Port,
		"zstd,zlib,snappy",
		"primary",
		"majority",
		"30000",
	)
}

// ensureDBSchema checks that we have all collections and indexes we need and
// creates them if needed.
// See https://docs.mongodb.com/manual/indexes/
// See https://docs.mongodb.com/manual/core/index-unique/
func ensureDBSchema(ctx context.Context, db *mongo.Database, log *logrus.Logger) error {
	// schema defines a mapping between a collection name and the indexes that
	// must exist for that collection.
	schema := map[string][]mongo.IndexModel{
		dbSkylinks: {
			{
				Keys:    bson.D{{"skylink", 1}},
				Options: options.Index().SetName("skylink").SetUnique(true),
			},
			{
				Keys:    bson.D{{"timestamp_added", 1}},
				Options: options.Index().SetName("timestamp_added"),
			},
		},
		dbLatestBlockTimestamps: {
			{
				Keys:    bson.D{{"server_name", 1}},
				Options: options.Index().SetName("server_name").SetUnique(true),
			},
		},
	}

	for collName, models := range schema {
		coll, err := ensureCollection(ctx, db, collName)
		if err != nil {
			return err
		}
		iv := coll.Indexes()
		var names []string
		names, err = iv.CreateMany(ctx, models)
		if err != nil {
			return errors.AddContext(err, "failed to create indexes")
		}
		log.Debugf("Ensured index exists: %v", names)
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
