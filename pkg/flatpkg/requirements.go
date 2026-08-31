// The pre-install requirements property list, which productbuild reads with
// --product and turns into the checks a Distribution runs before it will
// install anything.
//
// The mapping is productbuild's, measured one key at a time rather than
// taken from the manual, and each key drives exactly one element.
package flatpkg

import (
	"fmt"
	"strconv"
	"strings"

	"howett.net/plist"
)

// ProductRequirements mirrors the property list productbuild --product
// reads. Apple's manual still calls it the product definition property
// list in places.
type ProductRequirements struct {
	// OS lists the minimum system versions allowed. More than one exists
	// so that a given major release can require its own update: see
	// allowedOSVersions for how the ranges come out.
	OS []string `plist:"os"`
	// Arch is the supported architectures, and becomes hostArchitectures.
	Arch []string `plist:"arch"`
	// RAM is the minimum memory in gigabytes.
	RAM *float64 `plist:"ram"`
	// Bundle names bundles that must already be on the system.
	Bundle []RequiredBundle `plist:"bundle"`
	// AllBundles says whether every bundle is required or only one of
	// them. Absent means all.
	AllBundles *bool `plist:"all-bundles"`
	// The graphics predicates. productbuild passes each through
	// NSPredicate, which rewrites it: "version >= 2.0" comes back as
	// "version >= 2" and "isLowPowerDevice == NO" as
	// "isLowPowerDevice == 0". There is no NSPredicate here, so these are
	// written as given, which is the one place a generated Distribution
	// can differ from productbuild's.
	GLRenderer           string `plist:"gl-renderer"`
	CLDevice             string `plist:"cl-device"`
	MetalDevice          string `plist:"metal-device"`
	SingleGraphicsDevice *bool  `plist:"single-graphics-device"`
	// SysctlRequirements is a predicate over sysctl properties. Unlike the
	// graphics ones it is written through unchanged by productbuild too.
	SysctlRequirements string `plist:"sysctl-requirements"`
	// Home says whether the product may be installed into a user's home
	// directory as well as for everyone.
	Home *bool `plist:"home"`
}

// RequiredBundle is one entry of the bundle array.
type RequiredBundle struct {
	ID                         string `plist:"id"`
	Path                       string `plist:"path"`
	CFBundleShortVersionString string `plist:"CFBundleShortVersionString"`
	// Search says whether to look the bundle up by identifier when it is
	// not at Path, which suits an application the user may have moved.
	Search *bool `plist:"search"`
}

// ParseProductRequirements decodes a pre-install requirements property list.
func ParseProductRequirements(data []byte) (*ProductRequirements, error) {
	var r ProductRequirements
	if _, err := plist.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("flatpkg: unable to parse the requirements property list: %w", err)
	}
	for i, b := range r.Bundle {
		if b.ID == "" || b.Path == "" {
			return nil, fmt.Errorf("flatpkg: requirements bundle %d needs both id and path", i)
		}
	}
	return &r, nil
}

// MinSpecVersion is the minSpecVersion a Distribution needs to carry these
// requirements. Most keys are understood by the original specification;
// allowed-os-versions, required-bundles, ram and required-graphics are not.
//
// minOSVersion covers --min-os-version, which productbuild has no flag for
// and which reaches the same allowed-os-versions element. payloadFloor is
// the version a large payload forces, which reaches it too.
func (r *ProductRequirements) MinSpecVersion(minOSVersion, payloadFloor string) string {
	if minOSVersion != "" || payloadFloor != "" {
		return "2"
	}
	if r == nil {
		return "1"
	}
	if len(r.OS) > 0 || len(r.Bundle) > 0 || r.RAM != nil || r.hasGraphics() {
		return "2"
	}
	return "1"
}

func (r *ProductRequirements) hasGraphics() bool {
	return r != nil && (r.GLRenderer != "" || r.CLDevice != "" || r.MetalDevice != "")
}

// Architectures is what hostArchitectures should say, or nil for the
// default.
func (r *ProductRequirements) Architectures() []string {
	if r == nil {
		return nil
	}
	return r.Arch
}

// DomainsElement is the <domains> line, or empty where the requirements ask
// for none. Only home=true produces one, and it opens all three domains.
func (r *ProductRequirements) DomainsElement() string {
	if r == nil || r.Home == nil || !*r.Home {
		return ""
	}
	return `    <domains enable_localSystem="true" enable_anywhere="true" enable_currentUserHome="true"/>` + "\n"
}

// VolumeCheckElement is the <volume-check> block that sits with the rest of
// the document, and the one productbuild appends after everything else.
//
// Which of the two carries the version check depends on where the version
// came from. A version the requirements asked for is part of the document
// proper, written before the bundles. A version only implied by a graphics
// requirement is added afterwards, so it lands after the bundles, and where
// there are no bundles it lands in a volume-check of its own at the very
// end of the document. Both are productbuild's placements.
func (r *ProductRequirements) VolumeCheckElement(minOSVersion, payloadFloor string) (main, trailing string) {
	declared := allowedOSVersions(r.osList(minOSVersion, payloadFloor))
	implied, trailingImplied := "", false
	if len(declared) == 0 {
		implied = payloadFloor
		// A floor the payload forces is part of the document proper. One
		// only a graphics requirement implies is written afterwards.
		if floor := r.versionFloor(); floor != "" && compareVersions(floor, implied) > 0 {
			implied, trailingImplied = floor, true
		}
	}
	hasBundles := r != nil && len(r.Bundle) > 0

	if implied != "" && trailingImplied && !hasBundles {
		return "", wrapVolumeCheck(osVersionLines([]osRange{{Min: implied}}))
	}
	if implied != "" && !trailingImplied {
		declared = []osRange{{Min: implied}}
		implied = ""
	}

	var body strings.Builder
	body.WriteString(osVersionLines(declared))
	if hasBundles {
		all := "true"
		if r.AllBundles != nil && !*r.AllBundles {
			all = "false"
		}
		body.WriteString("        <required-bundles all=" + quoteAttr(all) + ">\n")
		for _, b := range r.Bundle {
			body.WriteString("            <bundle")
			// Attribute order is alphabetical, as Apple's serialiser
			// writes it, and search is written even when it is false.
			if b.CFBundleShortVersionString != "" {
				body.WriteString(" CFBundleShortVersionString=" + quoteAttr(b.CFBundleShortVersionString))
			}
			body.WriteString(" id=" + quoteAttr(b.ID))
			body.WriteString(" path=" + quoteAttr(b.Path))
			search := "false"
			if b.Search != nil && *b.Search {
				search = "true"
			}
			body.WriteString(" search=" + quoteAttr(search) + "/>\n")
		}
		body.WriteString("        </required-bundles>\n")
	}
	if implied != "" {
		body.WriteString(osVersionLines([]osRange{{Min: implied}}))
	}
	if body.Len() == 0 {
		return "", ""
	}
	return wrapVolumeCheck(body.String()), ""
}

// wrapVolumeCheck puts a body inside a volume-check element.
func wrapVolumeCheck(body string) string {
	if body == "" {
		return ""
	}
	return "    <volume-check>\n" + body + "    </volume-check>\n"
}

// osVersionLines renders an allowed-os-versions element, or nothing.
func osVersionLines(versions []osRange) string {
	if len(versions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("        <allowed-os-versions>\n")
	for _, v := range versions {
		b.WriteString("            <os-version min=" + quoteAttr(v.Min))
		if v.Before != "" {
			b.WriteString(" before=" + quoteAttr(v.Before))
		}
		b.WriteString("/>\n")
	}
	b.WriteString("        </allowed-os-versions>\n")
	return b.String()
}

// InstallationCheckElement is the <installation-check> block: the hardware
// the product needs, in productbuild's order of memory, graphics, then
// everything sysctl can answer.
func (r *ProductRequirements) InstallationCheckElement() string {
	if r == nil {
		return ""
	}
	var body strings.Builder
	if r.RAM != nil {
		// Two decimals, which is how productbuild writes it.
		fmt.Fprintf(&body, "        <ram min-gb=%s/>\n", quoteAttr(strconv.FormatFloat(*r.RAM, 'f', 2, 64)))
	}
	if r.hasGraphics() {
		body.WriteString("        <required-graphics")
		if r.SingleGraphicsDevice != nil && *r.SingleGraphicsDevice {
			body.WriteString(` single-device="true"`)
		}
		body.WriteString(">\n")
		for _, g := range []struct{ element, predicate string }{
			{"required-gl-renderer", r.GLRenderer},
			{"required-cl-device", r.CLDevice},
			{"required-metal-device", r.MetalDevice},
		} {
			if g.predicate == "" {
				continue
			}
			fmt.Fprintf(&body, "            <%s><![CDATA[%s]]></%s>\n", g.element, g.predicate, g.element)
		}
		body.WriteString("        </required-graphics>\n")
	}
	if r.SysctlRequirements != "" {
		body.WriteString("        <hardware-properties>\n")
		fmt.Fprintf(&body, "            <sysctl-requirements><![CDATA[%s]]></sysctl-requirements>\n", r.SysctlRequirements)
		body.WriteString("        </hardware-properties>\n")
	}
	if body.Len() == 0 {
		return ""
	}
	return "    <installation-check>\n" + body.String() + "    </installation-check>\n"
}

// Version floors that individual requirements imply. The Installer ignores
// a check the running system is too old to make, so productbuild refuses to
// let the product reach one: naming a Metal requirement means the product
// needs macOS 10.14.4 whatever else was asked for.
//
// The manual also claims sysctl-requirements raises the floor to 10.10.
// Current productbuild does not, and this follows the tool.
const (
	glRendererFloor = "10.6.8"
	clDeviceFloor   = "10.7"
	metalFloor      = "10.14.4"
)

// versionFloor is the highest floor these requirements imply, or empty.
func (r *ProductRequirements) versionFloor() string {
	if r == nil {
		return ""
	}
	floor := ""
	raise := func(v string) {
		if floor == "" || compareVersions(v, floor) > 0 {
			floor = v
		}
	}
	if r.GLRenderer != "" {
		raise(glRendererFloor)
	}
	if r.CLDevice != "" {
		raise(clDeviceFloor)
	}
	if r.MetalDevice != "" {
		raise(metalFloor)
	}
	return floor
}

// osList merges the requirements' os key with a plain minimum version, so
// --min-os-version keeps working with no requirements property list, and
// applies whatever floor the other requirements imply.
//
// A version below the floor is dropped rather than raised: asking for
// 10.13 and 12.0 with a Metal requirement leaves 12.0 alone and drops
// 10.13 entirely, because there is no 10.13 that could run the check.
func (r *ProductRequirements) osList(minOSVersion, payloadFloor string) []string {
	declared := minOSVersion
	var versions []string
	switch {
	case r != nil && len(r.OS) > 0:
		versions = r.OS
	case declared != "":
		versions = []string{declared}
	}
	floor := r.versionFloor()
	if payloadFloor != "" && (floor == "" || compareVersions(payloadFloor, floor) > 0) {
		floor = payloadFloor
	}
	if floor == "" {
		return versions
	}
	var kept []string
	for _, v := range versions {
		if compareVersions(v, floor) >= 0 {
			kept = append(kept, v)
		}
	}
	// Where nothing survives, the floor is the requirement, but it is not
	// substituted here: VolumeCheckElement writes an implied floor in a
	// different place from a version that was asked for.
	return kept
}

// osRange is one <os-version> line.
type osRange struct{ Min, Before string }

// allowedOSVersions turns the os key into the ranges productbuild writes.
//
// The highest version is unbounded, and every other one is capped just
// below the next release of its own line, so naming 13.0 and 14.2 allows
// 13.0 up to 13.1 and 14.2 upwards, and nothing between. The cap is the
// version's second component incremented and the rest dropped, which is
// why 10.5.4 comes out as "before 10.6" while 13.4 comes out as
// "before 13.5".
//
// The highest comes first; the rest keep the order they were given in.
func allowedOSVersions(versions []string) []osRange {
	if len(versions) == 0 {
		return nil
	}
	highest := 0
	for i := range versions {
		if compareVersions(versions[i], versions[highest]) > 0 {
			highest = i
		}
	}
	out := []osRange{{Min: versions[highest]}}
	for i, v := range versions {
		if i == highest {
			continue
		}
		out = append(out, osRange{Min: v, Before: nextMinorAfter(v)})
	}
	return out
}

// nextMinorAfter increments a version's second component and drops the
// rest: 10.5.4 becomes 10.6, and 13.4 becomes 13.5.
func nextMinorAfter(v string) string {
	parts := strings.Split(v, ".")
	for len(parts) < 2 {
		parts = append(parts, "0")
	}
	minor := leadingInt(parts[1])
	if minor < 0 {
		minor = 0
	}
	return parts[0] + "." + strconv.Itoa(minor+1)
}

// compareVersions orders two dotted versions numerically.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x = max(leadingInt(as[i]), 0)
		}
		if i < len(bs) {
			y = max(leadingInt(bs[i]), 0)
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// quoteAttr renders an XML attribute value with its quotes.
func quoteAttr(v string) string { return `"` + xmlEscape(v) + `"` }
