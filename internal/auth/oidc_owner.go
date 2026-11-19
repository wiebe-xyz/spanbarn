package auth

import "strings"

// OwnerOrganization is the organization of the owner, the first of the
// configured ones. Its members read every project; the others read only the
// projects that list them (#557).
func (c *OIDCClient) OwnerOrganization() string {
	owner, _, _ := strings.Cut(c.cfg.RequiredOrganizationID, ",")
	return strings.TrimSpace(owner)
}
