# Security

This source implements Engine's EaaS trust service. On-player deployments with
`SC_LOCAL_IP` set reject peers that are not configured addresses on the local
unit. Docker deployments leave `SC_LOCAL_IP` empty and rely on host firewall
rules.

It does not implement SoundCloud Go+, account OAuth, Widevine, or FairPlay.
Only public, non-DRM streams are exposed.

The gRPC and HTTP protocols are unauthenticated. On-player deployments bind
them to the dedicated source alias and reject non-local peers. Docker
deployments have the local-peer filter disabled and must firewall 50010/TCP
and 50020/TCP; never expose them to the internet.
