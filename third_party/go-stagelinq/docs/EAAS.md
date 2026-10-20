# EAAS

## GRPC

The GRPC server listens on port 50010.

### `networktrust.v1.NetworkTrustService/CreateTrust`

#### Request parameters

- `string ed25519_pk` - Ed25519 public key of the device requesting trust
- `uint32 wireguard_port` - Requested WireGuard port
- `string device_name` - Name of the device requesting trust

#### Response

After the client passes any local-peer gate, gRPC status is 0 and the response
is Granted, Denied, or Busy. A local-only deployment may reject other peers
with gRPC `PermissionDenied` before this handler runs.

### `enginelibrary.v1.EngineLibraryService/GetLibrary`
### `enginelibrary.v1.EngineLibraryService/EventStream`
### `enginelibrary.v1.EngineLibraryService/GetTracks`
### `enginelibrary.v1.EngineLibraryService/GetTrack`
### `enginelibrary.v1.EngineLibraryService/PutEvents`
### `enginelibrary.v1.EngineLibraryService/SearchTracks`
### `enginelibrary.v1.EngineLibraryService/GetSearchFilters`
### `enginelibrary.v1.EngineLibraryService/GetLibraries`
### `enginelibrary.v1.EngineLibraryService/GetHistorySessions`
### `enginelibrary.v1.EngineLibraryService/GetHistoryPlayedTracks`
### `enginelibrary.v1.EngineLibraryService/GetCredentials`



## HTTP

The HTTP server listens on port 50020.

### `GET /ping`

Returns an empty `200 OK` response to ensure the client the server is still
reachable.

### `GET /download/:path`

Provide a given audio file.

#### Request parameters

- `:path` - Full path to audio file to deliver. Normally URL-encoded.

#### Response

The response is the audio file itself.

#### Remarks

Server should return proper `Content-length`, however `Content-type` does not
seem to matter as it can be set to `application/octet-stream`.
