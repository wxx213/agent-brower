package assets

import "embed"

// Browser archives are added by scripts/build.py for standalone releases.
//
//go:embed browser/* SKILL.md handoff.html THIRD_PARTY_NOTICES.txt
var Files embed.FS
