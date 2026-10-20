// soundcloak-resolve uses soundcloak's lib/sc directly to resolve a
// SoundCloud track and select a non-DRM stream. Output is JSON so the shell
// client can consume it.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"git.maid.zone/stuff/soundcloak/lib/cfg"
	"git.maid.zone/stuff/soundcloak/lib/sc"
)

type result struct {
	Title    string `json:"title"`
	Policy   string `json:"policy"`
	Protocol string `json:"protocol"`
	Preset   string `json:"preset"`
	Quality  string `json:"quality"`
	URL      string `json:"url"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: soundcloak-resolve <soundcloud-url|user/track|id>")
		os.Exit(2)
	}

	t, err := sc.GetArbitraryTrack(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}

	tr := t.Media.SelectCompatibleProgressive(cfg.DefaultPreferences)
	if tr == nil {
		tr = t.Media.SelectCompatibleRestream("mpeg")
	}
	if tr == nil {
		tr = t.Media.SelectCompatibleHLS("mpeg")
	}
	if tr == nil {
		fmt.Fprintln(os.Stderr, "no compatible non-DRM stream")
		os.Exit(1)
	}

	st, err := tr.GetStream("", t)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream:", err)
		os.Exit(1)
	}
	url := ""
	if st.Value.Playlist != nil {
		url = st.Value.Playlist.String()
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "empty stream URL")
		os.Exit(1)
	}

	json.NewEncoder(os.Stdout).Encode(result{
		Title:    t.Title,
		Policy:   string(t.Policy),
		Protocol: string(tr.Format.Protocol),
		Preset:   tr.Preset,
		Quality:  tr.Quality,
		URL:      url,
	})
}
