package jail

import (
	"regexp"
	"strconv"
)

// freebsdReleaseRe matches the release part of an image name or an OS version:
// "freebsd-14.3-RELEASE-amd64", "14.3-RELEASE", "14.3-STABLE", "15.0-CURRENT".
var freebsdReleaseRe = regexp.MustCompile(`(\d+)\.(\d+)-(RELEASE|STABLE|CURRENT|BETA\d*|RC\d*|PRERELEASE)`)

// freebsdUserlandVersion derives the jail's own OS identity from the image it
// was built from, or from an explicitly declared version.
//
// A jail inherits osrelease and osreldate from the host kernel unless they are
// set, so a 14.3 userland on a 15.1 host reports 15.1 to everything inside it.
// pkg then resolves its ABI as FreeBSD:15 and refuses the FreeBSD:14 repository
// the jail actually needs — "repository contains packages for wrong OS version"
// — which breaks package installation in every jail whose base does not match
// the host.
//
// The returned reldate follows __FreeBSD_version: major*100000 + minor*1000,
// which is what 14.3-RELEASE (1403000) and 15.1-RELEASE (1501000) report.
// An empty release means the version could not be determined, and the caller
// should leave the parameters alone rather than guess.
func freebsdUserlandVersion(image, osVersion string) (release string, reldate int) {
	for _, candidate := range []string{osVersion, image} {
		match := freebsdReleaseRe.FindStringSubmatch(candidate)
		if match == nil {
			continue
		}

		major, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}

		return match[0], major*100000 + minor*1000
	}

	return "", 0
}
