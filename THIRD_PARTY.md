# Third-party sources

## go-stagelinq

Repository: `github.com/icedream/go-stagelinq`

Commit: `c6b1b11eafb3aec578a2df7ba033d77c6e79efbd`

Local change: `enginelibrary.v1.EngineLibraryService.EventStream`,
`GetTracks`, and `SearchTracks` were regenerated as the server-streaming RPCs
declared by Engine 4.3.4's embedded protobuf descriptors. Discovery replies
also support selecting the source address with `IP_PKTINFO`, allowing an
on-player source to advertise a secondary LAN alias.

See `third_party/go-stagelinq/LICENSE`.

## Soundcloak

Repository: `git.maid.zone/stuff/soundcloak`

Commit: `a834f2e2d74b05fb687f5ce31abc8945a24a4d69`

Vendored Markdown was refreshed to match the checked-in defaults, preference
fields, audio preset fallback order, and TLS defaults.
`lib/sc.User.GetPlaylistLikes` was added for SoundCloud's dedicated
`/users/<id>/playlist_likes` feed.

See `third_party/soundcloak/LICENSE`.
