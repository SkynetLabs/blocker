# blocker

Blocker is a service that blocklists all abusive skylinks on the current server.
The service exposes a REST API that allows callers to request the blocking of
new skylinks. The blocklist is shared between the servers that make up a portal
cluster via MongoDB.

# Hashes

The blocker will convert the Skylink to a hash of its merkle root as soon as
possible. This to prevent the persistence of abusive skylinks in the database
and/or log files.

# AllowList

The blocker service can only block skylinks which are not in the allow list.
To add a skylink to the allow list, one has to manually query the database and
perform the follow operation:

```
db.getCollection('allowlist').insertOne({
  skylink: "[INSERT V1 SKYLINK HERE]",
  description: "[INSERT SKYLINK DESCRIPTION]",
  timestamp_added: new Date(),
})
```

The skylink is expected to be in the following form: `_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA`.
So that's without portal and without the `sia://` prefix. The allow list is
persisted as is, so not as a hash, for ease of use and because it is assumed the
allowlist only holds non-abusive content.

# Environment

This service depends on the following environment variables:
* `API_HOST`, defaults to `sia`
* `API_PORT`, defaults to `9980`
* `SIA_API_PASSWORD`
* `NGINX_HOST`, defaults to `10.10.10.30`
* `NGINX_PORT`, defaults to `8000`
* `SKYNET_DB_HOST`
* `SKYNET_DB_PORT`
* `SKYNET_DB_USER`
* `SKYNET_DB_PASS`
* `SKYNET_ACCOUNTS_HOST`, defaults to `accounts`
* `SKYNET_ACCOUNTS_PORT`, defaults to `3000`
* `SERVER_DOMAIN`, e.g. `eu-ger-5.siasky.net`
* `BLOCKER_LOG_LEVEL`, defaults to `info`