// SPDX-License-Identifier: AGPL-3.0-or-later

package proxmox

// Product identifies a member of the Proxmox product family. All four share the
// /api2/json REST API but differ in default port, session-cookie name, and which
// API-token authentication scheme (if any) they support.
type Product string

const (
	PVE Product = "pve" // Proxmox VE
	PBS Product = "pbs" // Proxmox Backup Server
	PMG Product = "pmg" // Proxmox Mail Gateway
	PDM Product = "pdm" // Proxmox Datacenter Manager (aggregates remote PVE/PBS)
)

// productSpec captures the per-product differences the rest of the client is
// otherwise agnostic to.
type productSpec struct {
	defaultPort int
	cookieName  string // session cookie name for ticket auth
	tokenPrefix string // Authorization scheme prefix for API tokens ("" → no token support)
	tokenSep    string // separator between the token id and its secret
}

// supportsToken reports whether the product accepts API-token auth (PMG does not).
func (s productSpec) supportsToken() bool { return s.tokenPrefix != "" }

// authorization builds the Authorization header value for API-token auth, e.g.
// PVE -> "PVEAPIToken=user@realm!id=secret"; PBS -> "PBSAPIToken=user@realm!id:secret".
func (s productSpec) authorization(tokenID, secret string) string {
	return s.tokenPrefix + tokenID + s.tokenSep + secret
}

// specFor returns the spec for a product and whether it is known.
func specFor(p Product) (productSpec, bool) {
	switch p {
	case PVE:
		return productSpec{defaultPort: 8006, cookieName: "PVEAuthCookie", tokenPrefix: "PVEAPIToken=", tokenSep: "="}, true
	case PBS:
		// PBS uses a colon between token id and secret (PVE uses '=').
		return productSpec{defaultPort: 8007, cookieName: "PBSAuthCookie", tokenPrefix: "PBSAPIToken=", tokenSep: ":"}, true
	case PMG:
		// PMG is ticket-only — no API tokens.
		return productSpec{defaultPort: 8006, cookieName: "PMGAuthCookie"}, true
	case PDM:
		// PDM (Datacenter Manager) — verified against pdm-lab 1.1.1: it does NOT
		// reuse the PVE scheme. Default API port 8443, session cookie
		// __Host-PDMAuthCookie, and an API-token scheme using the PDM prefix with
		// a ':' secret separator (PBS-style, not PVE's '='):
		// PDMAPIToken=user@realm!id:secret.
		return productSpec{defaultPort: 8443, cookieName: "__Host-PDMAuthCookie", tokenPrefix: "PDMAPIToken=", tokenSep: ":"}, true
	default:
		return productSpec{}, false
	}
}

// Valid reports whether p is a recognized product.
func (p Product) Valid() bool {
	_, ok := specFor(p)
	return ok
}

// SupportsToken reports whether the product accepts API-token authentication.
func (p Product) SupportsToken() bool {
	s, ok := specFor(p)
	return ok && s.supportsToken()
}
