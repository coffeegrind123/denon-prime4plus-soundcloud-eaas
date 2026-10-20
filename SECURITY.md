# Security

This source implements Engine's EaaS trust service and grants any local peer
that completes the handshake. Run it only on a trusted DJ LAN.

It does not implement SoundCloud Go+, account OAuth, Widevine, or FairPlay.
Only public, non-DRM streams are exposed.

The HTTP service is unauthenticated, like the Engine EaaS contract it
implements. Firewall ports 50010/TCP and 50020/TCP to trusted hosts and do not
expose them to the internet.
